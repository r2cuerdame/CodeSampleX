package main

import (
	"context"
	"fmt"

	"github.com/r2cuerdame/codesamplex/internal/httpapi"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// bootMaintenanceSteps is the maintenance lane's content (#250): the
// boot-time reconciles and the dedup purge that runServe used to run inline,
// each as one budgeted bootStep. The row limits are the same constants as
// before; what each step returns is the sentence runServe used to print.
func bootMaintenanceSteps(cfg serverstore.ServerConfig, pg *serverstore.PG, budget bootMaintenanceBudget) []bootStep {
	steps := []bootStep{
		{
			// Wake authoring drafts that have nothing left to wait for.
			//
			// A verifier that cannot resolve dependencies files a SKIPPED
			// receipt, which closes the sample's only cross job without
			// measuring anything. The receipt path queues another attempt
			// now, but the drafts stranded before that existed have no
			// future event to reach them -- production held 159, verified
			// by nobody and waiting on nothing. Boot is a good enough clock
			// for a finite backlog, and it keeps this off every request
			// path.
			Name:   "stranded-drafts",
			Budget: budget.StrandedDrafts,
			Run: func(ctx context.Context) (string, error) {
				woken, err := httpapi.ReconcileStrandedDrafts(ctx, pg, strandedReconcileLimit)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("requeued %d stranded authoring drafts", woken), nil
			},
		},
		{
			// Bring the open cross queue back in line with the images this
			// build pins. A job may only ask for a lane the fleet has; three
			// that asked for Go 1.27 -- a contributor's toolchain, never a
			// verifier image -- sat open and unclaimable while every worker
			// reported no work.
			Name:   "cross-job-lanes",
			Budget: budget.CrossJobLanes,
			Run: func(ctx context.Context) (string, error) {
				repaired, unsupported, err := httpapi.ReconcileCrossJobLanes(ctx, pg, laneReconcileLimit)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("repaired %d cross jobs, recorded %d as unsupported", repaired, unsupported), nil
			},
		},
	}
	// Resolve publicness for coordinates that arrived without being checked
	// (#176). Packages seeded early or ingested past the per-request lookup
	// budget were stored with checked_at IS NULL and refused on every
	// subsequent evidence upload. Reconciling them at boot settles their
	// publicness and clears the refusal loop without charging active
	// clients. This is the one step that leaves the machine (package
	// registries), which is why its budget is among the largest.
	if cfg.PublicCheck != "trust" {
		steps = append(steps, bootStep{
			Name:   "publicness",
			Budget: budget.Publicness,
			Run: func(ctx context.Context) (string, error) {
				checker := &registry.Checker{Cache: &registry.ServerCache{Store: pg}}
				checked, err := httpapi.ReconcileUncheckedPublicness(ctx, pg, checker, 500)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("checked and resolved publicness for %d unverified packages", checked), nil
			},
		})
	}
	steps = append(steps,
		bootStep{
			// Reconcile the dependency atlas from verified sample receipts
			// (#185). Coordinates proven in single-package recipes or
			// explicit recipe dependencies are backfilled into
			// dependency_resolution (DependsOnNone) or dependency_edge,
			// which unblocks dependency closure work. Before #250 this ran
			// as its own goroutine beside everything else at boot; it is
			// now a lane step like the rest.
			Name:   "dependency-atlas",
			Budget: budget.DependencyAtlas,
			Run: func(ctx context.Context) (string, error) {
				reconciled, err := httpapi.ReconcileDependencyAtlas(ctx, pg, 2000)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("reconciled %d dependency observations into atlas", reconciled), nil
			},
		},
		bootStep{
			// Apply the dedup retention window.
			//
			// PurgeDedupOlderThan has existed with a documented 30-day
			// window and no caller at all -- docs/data-rights.md says so
			// outright. A retention policy this project states to its
			// contributors was not being applied to their data. Measured on
			// production 2026-09-01: 553,823 dedup rows across 21 epochs,
			// of which 1,102 were past the window; it becomes load-bearing
			// as the corpus ages past thirty days. Aggregates are
			// untouched: only the rotating bucket linkage goes, so
			// unique_*_buckets freeze at their accumulated values rather
			// than shrinking retroactively.
			Name:   "dedup-purge",
			Budget: budget.DedupPurge,
			Run: func(ctx context.Context) (string, error) {
				removed, err := pg.PurgeDedupOlderThan(ctx, dedupRetentionDays)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("purged %d dedup buckets older than %d days", removed, dedupRetentionDays), nil
			},
		},
	)
	return steps
}
