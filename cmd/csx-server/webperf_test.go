package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type perfCountingStore struct {
	*serverstore.Fake
	failureClusterCalls      atomic.Int64
	completenessGapsCalls    atomic.Int64
	searchSamplesCalls       atomic.Int64
	dependencySubjectCalls   atomic.Int64
	dependenciesCalls        atomic.Int64
	snapshotKeysCalls        atomic.Int64
	listPackageVersionsCalls atomic.Int64
}

func (s *perfCountingStore) ListFailureClusters(ctx context.Context, packageName string) ([]serverstore.ClusterRow, error) {
	s.failureClusterCalls.Add(1)
	return s.Fake.ListFailureClusters(ctx, packageName)
}

func (s *perfCountingStore) CompletenessGaps(ctx context.Context, query string, offset, limit int) ([]serverstore.CompletenessGap, int, error) {
	s.completenessGapsCalls.Add(1)
	return s.Fake.CompletenessGaps(ctx, query, offset, limit)
}

func (s *perfCountingStore) SearchSamplesPage(ctx context.Context, query string, limit, offset int) ([]serverstore.SampleRow, int, error) {
	s.searchSamplesCalls.Add(1)
	return s.Fake.SearchSamplesPage(ctx, query, limit, offset)
}

func (s *perfCountingStore) DependencySubjects(ctx context.Context, query string, offset, limit int) ([]serverstore.DependencySubject, int, error) {
	s.dependencySubjectCalls.Add(1)
	return s.Fake.DependencySubjects(ctx, query, offset, limit)
}

func (s *perfCountingStore) Dependencies(ctx context.Context, ecosystem, name string) ([]serverstore.DependencyEdge, error) {
	s.dependenciesCalls.Add(1)
	return s.Fake.Dependencies(ctx, ecosystem, name)
}

func (s *perfCountingStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	s.snapshotKeysCalls.Add(1)
	return s.Fake.SnapshotKeys(ctx)
}

func (s *perfCountingStore) ListPackageVersions(ctx context.Context, ecosystem, name string) ([]serverstore.PackageRow, error) {
	s.listPackageVersionsCalls.Add(1)
	return s.Fake.ListPackageVersions(ctx, ecosystem, name)
}

func TestWebStoreCachesFailureClusters(t *testing.T) {
	ctx := context.Background()
	fake := serverstore.NewFake()
	mock := &perfCountingStore{Fake: fake}
	w := &webStore{s: mock}

	if err := fake.UpsertFailureClusters(ctx, []serverstore.ClusterRow{
		{
			Ecosystem:        "npm",
			PackageName:      "axios",
			Symbol:           "axios.get",
			Stage:            "VERIFY",
			ErrorFingerprint: "fp1",
			ObservationCount: 5,
			FirstSeen:        time.Now(),
			LastSeen:         time.Now(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		docs, total, err := w.FailureClusters(ctx, "npm", "axios")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if total != 1 || len(docs) != 1 {
			t.Fatalf("call %d: expected 1 cluster doc, got %d (total %d)", i, len(docs), total)
		}
	}

	if got := mock.failureClusterCalls.Load(); got != 1 {
		t.Errorf("expected exactly 1 call to store.ListFailureClusters, got %d", got)
	}
}

func TestWebStoreCachesCompletenessGapsAcrossQueriesAndPages(t *testing.T) {
	ctx := context.Background()
	fake := serverstore.NewFake()
	mock := &perfCountingStore{Fake: fake}
	w := &webStore{s: mock}

	// First call loads whole corpus into memory.
	rows1, _, err := w.CompletenessGaps(ctx, "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	_ = rows1

	// Subsequent calls with different queries and offsets must hit the memory cache.
	for _, tc := range []struct {
		query  string
		offset int
		limit  int
	}{
		{"", 10, 10},
		{"sys", 0, 10},
		{"sys", 10, 10},
		{"other", 0, 5},
	} {
		_, _, err := w.CompletenessGaps(ctx, tc.query, tc.offset, tc.limit)
		if err != nil {
			t.Fatalf("query=%q offset=%d: %v", tc.query, tc.offset, err)
		}
	}

	if got := mock.completenessGapsCalls.Load(); got != 1 {
		t.Errorf("expected exactly 1 call to store.CompletenessGaps across all queries/pages, got %d", got)
	}
}

func TestWebStoreCachesSearchSamples(t *testing.T) {
	ctx := context.Background()
	fake := serverstore.NewFake()
	mock := &perfCountingStore{Fake: fake}
	w := &webStore{s: mock}

	if err := fake.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     "sha256:test1",
		ManifestJSON: `{"goal":"prove sys call","packages":["pkg:golang/golang.org/x/sys@v0.1.0"]}`,
		Status:       "PUBLISHED",
		CreatedAt:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 4; i++ {
		items, total, err := w.SearchSamples(ctx, "sys", 0, 24)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if total != 1 || len(items) != 1 {
			t.Fatalf("call %d: expected 1 item, got %d (total %d)", i, len(items), total)
		}
	}

	if got := mock.searchSamplesCalls.Load(); got != 1 {
		t.Errorf("expected exactly 1 call to store.SearchSamplesPage, got %d", got)
	}
}

func TestWebStoreCachesDependencySubjects(t *testing.T) {
	ctx := context.Background()
	fake := serverstore.NewFake()
	mock := &perfCountingStore{Fake: fake}
	w := &webStore{s: mock}

	for i := 0; i < 4; i++ {
		_, _, err := w.DependencySubjects(ctx, "", 0, 50)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	if got := mock.dependencySubjectCalls.Load(); got != 1 {
		t.Errorf("expected exactly 1 call to store.DependencySubjects, got %d", got)
	}
}

func TestWebStoreCachesDependencies(t *testing.T) {
	ctx := context.Background()
	fake := serverstore.NewFake()
	mock := &perfCountingStore{Fake: fake}
	w := &webStore{s: mock}

	for i := 0; i < 4; i++ {
		_, err := w.Dependencies(ctx, "npm", "axios")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	if got := mock.dependenciesCalls.Load(); got != 1 {
		t.Errorf("expected exactly 1 call to store.Dependencies, got %d", got)
	}
}

func TestWebStoreIndexesSnapshotTargets(t *testing.T) {
	ctx := serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive)
	fake := serverstore.NewFake()
	if err := fake.PutSnapshot(ctx, "pkg:npm/axios@1.7.9", "axios.get", `{"rows":[]}`); err != nil {
		t.Fatal(err)
	}
	if err := fake.PutSnapshot(ctx, "pkg:npm/axios@1.7.9", "axios.post", `{"rows":[]}`); err != nil {
		t.Fatal(err)
	}
	if err := fake.PutSnapshot(ctx, "pkg:npm/express@4.18.2", "express.json", `{"rows":[]}`); err != nil {
		t.Fatal(err)
	}
	mock := &perfCountingStore{Fake: fake}
	w := &webStore{s: mock}

	// First call loads and builds index.
	syms, err := w.PackageSymbols(ctx, "npm", "axios", "1.7.9")
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 2 || syms[0] != "axios.get" || syms[1] != "axios.post" {
		t.Fatalf("unexpected symbols: %v", syms)
	}

	// Repeated calls must hit the indexed target map without reloading SnapshotKeys.
	for i := 0; i < 3; i++ {
		syms2, err := w.PackageSymbols(ctx, "npm", "axios", "1.7.9")
		if err != nil {
			t.Fatal(err)
		}
		if len(syms2) != 2 {
			t.Fatalf("call %d: unexpected symbols: %v", i, syms2)
		}
	}

	spread, err := w.SymbolPackageSpread(ctx, "npm", []string{"axios.get", "express.json"})
	if err != nil {
		t.Fatal(err)
	}
	if spread["axios.get"] != 1 || spread["express.json"] != 1 {
		t.Fatalf("unexpected spread: %v", spread)
	}

	if got := mock.snapshotKeysCalls.Load(); got != 1 {
		t.Errorf("expected exactly 1 call to store.SnapshotKeys, got %d", got)
	}
}
