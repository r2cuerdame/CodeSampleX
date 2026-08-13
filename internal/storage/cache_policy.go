// Package storage holds cross-cutting policy over the local stores: the
// SQLite metadata DB (localdb) and the content-addressed artifact cache
// (cas). Everything here is local-only; nothing in this package touches
// the network or upload paths.
package storage

import (
	"context"
	"os"
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// EnforceBudget evicts cached sample artifacts until the CAS fits inside
// budgetMB. Candidates are unpinned samples that still have an artifact,
// evicted coldest-first: lowest hot_score, then least recently used
// (goal.md §11.2 HOT cache policy). Pinned samples are never evicted, even
// if the store stays over budget. Eviction removes only the artifact bytes
// (cas.Delete + has_artifact=0); sample metadata and receipts remain.
func EnforceBudget(ctx context.Context, db *localdb.DB, store *cas.Store, budgetMB int) (evicted []string, err error) {
	budget := int64(budgetMB) * 1024 * 1024
	total, err := store.TotalSize()
	if err != nil {
		return nil, err
	}
	if total <= budget {
		return nil, nil
	}

	rows, err := db.ListSamples(ctx)
	if err != nil {
		return nil, err
	}
	var candidates []localdb.SampleRow
	for _, r := range rows {
		if r.HasArtifact && !r.Pinned {
			candidates = append(candidates, r)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.HotScore != b.HotScore {
			return a.HotScore < b.HotScore
		}
		if !a.LastUsed.Equal(b.LastUsed) {
			return a.LastUsed.Before(b.LastUsed)
		}
		return a.SampleID < b.SampleID
	})

	for _, r := range candidates {
		if total <= budget {
			break
		}
		if err := store.Delete(r.SampleID); err != nil && !os.IsNotExist(err) {
			return evicted, err
		}
		r.HasArtifact = false
		if err := db.SaveSample(ctx, r); err != nil {
			return evicted, err
		}
		evicted = append(evicted, r.SampleID)
		if total, err = store.TotalSize(); err != nil {
			return evicted, err
		}
	}
	return evicted, nil
}
