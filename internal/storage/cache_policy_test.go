package storage

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// putArtifact stores size bytes of unique content and registers a sample row.
func putArtifact(t *testing.T, ctx context.Context, db *localdb.DB, store *cas.Store,
	seed byte, size int, pinned bool, hotScore float64, lastUsed time.Time) string {
	t.Helper()
	content := bytes.Repeat([]byte{seed}, size)
	id, err := store.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("cas put: %v", err)
	}
	err = db.SaveSample(ctx, localdb.SampleRow{
		SampleID:     id,
		ManifestJSON: "{}",
		Status:       "PUBLISHED",
		License:      "MIT-0",
		Pinned:       pinned,
		HotScore:     hotScore,
		LastUsed:     lastUsed,
		HasArtifact:  true,
	})
	if err != nil {
		t.Fatalf("save sample: %v", err)
	}
	return id
}

func openStores(t *testing.T) (*localdb.DB, *cas.Store) {
	t.Helper()
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatalf("open localdb: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := cas.Open(filepath.Join(dir, "cas"))
	if err != nil {
		t.Fatalf("open cas: %v", err)
	}
	return db, store
}

func TestEnforceBudgetEvictionOrder(t *testing.T) {
	ctx := context.Background()
	db, store := openStores(t)

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	const sz = 600_000 // 3 × 600KB = 1.8MB total
	cold := putArtifact(t, ctx, db, store, 'a', sz, false, 0.1, base)
	warm := putArtifact(t, ctx, db, store, 'b', sz, false, 0.5, base.Add(24*time.Hour))
	hot := putArtifact(t, ctx, db, store, 'c', sz, false, 5.0, base.Add(48*time.Hour))

	// Budget 1MB: evicting cold (→1.2MB) is not enough; warm goes too
	// (→600KB ≤ 1MB); hot survives.
	evicted, err := EnforceBudget(ctx, db, store, 1)
	if err != nil {
		t.Fatalf("EnforceBudget: %v", err)
	}
	if len(evicted) != 2 || evicted[0] != cold || evicted[1] != warm {
		t.Fatalf("evicted = %v, want [%s %s]", evicted, cold, warm)
	}
	if store.Has(cold) || store.Has(warm) {
		t.Fatalf("evicted artifacts still present in CAS")
	}
	if !store.Has(hot) {
		t.Fatalf("highest-score artifact must survive")
	}
	for _, id := range []string{cold, warm} {
		row, ok, err := db.GetSample(ctx, id)
		if err != nil || !ok {
			t.Fatalf("sample row %s: ok=%v err=%v", id, ok, err)
		}
		if row.HasArtifact {
			t.Fatalf("evicted sample %s still marked has_artifact", id)
		}
	}
	row, _, _ := db.GetSample(ctx, hot)
	if !row.HasArtifact {
		t.Fatalf("surviving sample lost has_artifact flag")
	}
}

func TestEnforceBudgetLastUsedTieBreak(t *testing.T) {
	ctx := context.Background()
	db, store := openStores(t)

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	const sz = 700_000 // 2 × 700KB = 1.4MB
	older := putArtifact(t, ctx, db, store, 'a', sz, false, 1.0, base)
	newer := putArtifact(t, ctx, db, store, 'b', sz, false, 1.0, base.Add(time.Hour))

	// Budget 1MB: same hot_score, so the least recently used goes first,
	// and one eviction is enough.
	evicted, err := EnforceBudget(ctx, db, store, 1)
	if err != nil {
		t.Fatalf("EnforceBudget: %v", err)
	}
	if len(evicted) != 1 || evicted[0] != older {
		t.Fatalf("evicted = %v, want [%s]", evicted, older)
	}
	if !store.Has(newer) {
		t.Fatalf("more recently used artifact must survive")
	}
}

func TestEnforceBudgetNeverEvictsPinned(t *testing.T) {
	ctx := context.Background()
	db, store := openStores(t)

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	const sz = 400_000
	// Pinned sample has the WORST score — still immune.
	pinned := putArtifact(t, ctx, db, store, 'p', sz, true, 0.0, base)
	a := putArtifact(t, ctx, db, store, 'a', sz, false, 1.0, base)
	b := putArtifact(t, ctx, db, store, 'b', sz, false, 2.0, base)

	// Budget 0: everything unpinned must go; pinned stays even though the
	// store remains over budget.
	evicted, err := EnforceBudget(ctx, db, store, 0)
	if err != nil {
		t.Fatalf("EnforceBudget: %v", err)
	}
	if len(evicted) != 2 || evicted[0] != a || evicted[1] != b {
		t.Fatalf("evicted = %v, want [%s %s]", evicted, a, b)
	}
	if !store.Has(pinned) {
		t.Fatalf("pinned artifact was evicted")
	}
	row, _, _ := db.GetSample(ctx, pinned)
	if !row.HasArtifact || !row.Pinned {
		t.Fatalf("pinned sample row mutated: %+v", row)
	}
}

func TestEnforceBudgetUnderBudgetNoop(t *testing.T) {
	ctx := context.Background()
	db, store := openStores(t)
	id := putArtifact(t, ctx, db, store, 'a', 1000, false, 0.0, time.Time{})

	evicted, err := EnforceBudget(ctx, db, store, 10)
	if err != nil {
		t.Fatalf("EnforceBudget: %v", err)
	}
	if len(evicted) != 0 {
		t.Fatalf("nothing should be evicted under budget, got %v", evicted)
	}
	if !store.Has(id) {
		t.Fatalf("artifact removed while under budget")
	}
}
