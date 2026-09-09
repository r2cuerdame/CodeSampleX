package httpapi

import (
	"context"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// maxDependencyProbesPerRequest bounds how many NEW dependency coordinates one
// poll of /v1/authoring/work/next may check against a public registry.
//
// Dependency work is the one queue source whose coordinates the network has
// never held a package row for: they exist because somebody's lockfile
// resolved onto them, and nothing has confirmed them since. Confirming a
// window's worth would be up to two hundred sequential probes at npmjs.org
// per poll, from a fleet that polls several times a minute — which is how a
// host gets blocked, and it would hold the handler open for minutes while it
// happened.
//
// Four is enough. A worker takes ONE job per poll, and the candidate order is
// stable, so successive polls walk further down the list while everything
// already confirmed costs nothing: a confirmed coordinate leaves a PUBLIC
// package row behind and is free from then on. The unconfirmed remainder is
// dropped from this pass rather than refused, which is the difference between
// "not yet asked" and "answered no".
const maxDependencyProbesPerRequest = 4

// dependencyLookupBatch bounds how many dependency coordinates share one
// packages-table lookup. The candidate window is a few hundred rows; one
// unbounded array parameter would only move the same work into one breath.
const dependencyLookupBatch = 500

// confirmDependencyWork keeps the DEPENDENCY candidates a public registry
// confirms and drops the rest of them from this pass.
//
// It exists because a dependency coordinate is the only work this server
// hands out that no publicness gate has already seen. The ingest gate checks
// every dependsOn child, but only outside trust mode and only within its own
// per-request lookup cap, so an edge can reach the table unconfirmed. Absolute
// principle 2 limits automatic collection to packages that exist on a public
// registry, and a coordinate this server tells a worker to build is squarely
// inside that boundary.
//
// The confirmation is not only a filter. registry.Checker writes its verdict
// through to the packages table, so confirming a coordinate is also what
// registers it: the release stops being an edge nobody has a row for and
// becomes a package the registry endpoints, the version axis and the next
// scheduling pass can all see.
func (a *api) confirmDependencyWork(ctx context.Context, candidates []serverstore.WantedRow) []serverstore.WantedRow {
	hasDependency := false
	for _, c := range candidates {
		if c.Kind == "DEPENDENCY" && (c.Axis == "" || c.Axis == serverstore.AuthoringAxisSample) {
			hasDependency = true
			break
		}
	}
	if !hasDependency {
		return candidates
	}
	// Trust mode turns the publicness gate off for ingest too; it is dev and
	// e2e only, and a server running it has no checker to ask.
	if a.trustMode() {
		return candidates
	}
	// No checker and not trust mode: the server cannot tell whether any
	// coordinate is public, and UNKNOWN is private — the safe default. There
	// is nothing to look up, because nothing could be confirmed anyway.
	var public map[string]bool
	if a.d.Checker != nil {
		public = a.publicDependencyRows(ctx, candidates)
	}
	out := make([]serverstore.WantedRow, 0, len(candidates))
	probes := 0
	for _, c := range candidates {
		if !isUnconfirmedDependency(c) {
			out = append(out, c)
			continue
		}
		if a.d.Checker == nil {
			continue
		}
		p := domain.PURL{Ecosystem: c.Ecosystem, Name: c.Name, Version: c.Version}
		if public[p.String()] {
			// Already a registered public release. Asking again would spend a
			// registry round trip to learn what this server wrote down itself.
			out = append(out, c)
			continue
		}
		if probes >= maxDependencyProbesPerRequest {
			continue
		}
		probes++
		if a.d.Checker.Check(ctx, p) != scanner.PublicnessPublic {
			continue
		}
		out = append(out, c)
	}
	return out
}

// isUnconfirmedDependency reports whether the pass confirms this candidate:
// dependency work on any axis but the dependency axis itself, which is the
// predicate the filter below has always applied.
func isUnconfirmedDependency(c serverstore.WantedRow) bool {
	return c.Kind == "DEPENDENCY" && c.Axis != serverstore.AuthoringAxisDependency
}

// packageRowsStore is the bounded-page form of GetPackage. PostgreSQL
// offers it; the fallback below keeps the row-at-a-time contract for stores
// that do not.
type packageRowsStore interface {
	PackagesByPURL(ctx context.Context, purls []string) (map[string]serverstore.PackageRow, error)
}

// publicDependencyRows reports which dependency candidates already have a
// PUBLIC package row, so that they pass without a registry probe.
//
// It asks the packages table once per bounded page rather than once per
// candidate. The window is a few hundred coordinates and this endpoint is
// polled several times a minute by every worker in the fleet, so the
// per-candidate read was a few hundred interactive pool checkouts per poll --
// work that scaled with the queue rather than with the one job handed out
// (#174).
//
// A read that fails answers nothing, exactly as the row-at-a-time read did:
// the coordinate is then treated as unconfirmed and falls under the bounded
// probe budget, never as public and never as private.
func (a *api) publicDependencyRows(ctx context.Context, candidates []serverstore.WantedRow) map[string]bool {
	purls := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if !isUnconfirmedDependency(c) {
			continue
		}
		purl := domain.PURL{Ecosystem: c.Ecosystem, Name: c.Name, Version: c.Version}.String()
		if seen[purl] {
			continue
		}
		seen[purl] = true
		purls = append(purls, purl)
	}
	public := make(map[string]bool, len(purls))
	bulk, ok := a.d.Store.(packageRowsStore)
	if !ok {
		for _, purl := range purls {
			if pkg, found, err := a.d.Store.GetPackage(ctx, purl); err == nil && found &&
				pkg.Publicness == scanner.PublicnessPublic {
				public[purl] = true
			}
		}
		return public
	}
	for start := 0; start < len(purls); start += dependencyLookupBatch {
		end := min(start+dependencyLookupBatch, len(purls))
		page, err := bulk.PackagesByPURL(ctx, purls[start:end])
		if err != nil {
			continue
		}
		for purl, pkg := range page {
			if pkg.Publicness == scanner.PublicnessPublic {
				public[purl] = true
			}
		}
	}
	return public
}
