package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/web"
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

type failingBulkSnapshotStore struct {
	*serverstore.Fake
	bulkCalls   atomic.Int64
	singleCalls atomic.Int64
}

func (s *failingBulkSnapshotStore) GetSnapshotsForPURL(context.Context, string) ([]serverstore.SnapshotRow, error) {
	s.bulkCalls.Add(1)
	return nil, serverstore.ErrPoolBusy
}

func (s *failingBulkSnapshotStore) GetSnapshot(ctx context.Context, purl, symbol string) (string, bool, error) {
	s.singleCalls.Add(1)
	return s.Fake.GetSnapshot(ctx, purl, symbol)
}

func TestSnapshotBulkFailureFailsFastWithoutSingletonFanout(t *testing.T) {
	store := &failingBulkSnapshotStore{Fake: serverstore.NewFake()}
	w := &webStore{s: store}
	ctx := serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive)
	purl := "pkg:golang/github.com/jackc/pgx/v5@v5.10.0"
	for _, symbol := range []string{"", "Batch", "CollectRows", "ParseConfig"} {
		if _, ok, err := w.SnapshotJSONWithError(ctx, purl, symbol); ok || err == nil {
			t.Fatalf("snapshot %q unexpectedly exists", symbol)
		}
	}
	if got := store.bulkCalls.Load(); got != 1 {
		t.Fatalf("bulk calls = %d, want 1 during retry backoff", got)
	}
	if got := store.singleCalls.Load(); got != 0 {
		t.Fatalf("singleton fallback calls = %d, want 0 after bulk pressure", got)
	}
}

type blockingBulkSnapshotStore struct {
	*failingBulkSnapshotStore
	startedOnce sync.Once
	started     chan struct{}
	release     chan struct{}
}

func (s *blockingBulkSnapshotStore) GetSnapshotsForPURL(ctx context.Context, _ string) ([]serverstore.SnapshotRow, error) {
	s.bulkCalls.Add(1)
	s.startedOnce.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return nil, serverstore.ErrPoolBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestConcurrentSnapshotMissesShareOneBulkAttempt(t *testing.T) {
	store := &blockingBulkSnapshotStore{
		failingBulkSnapshotStore: &failingBulkSnapshotStore{Fake: serverstore.NewFake()},
		started:                  make(chan struct{}),
		release:                  make(chan struct{}),
	}
	w := &webStore{s: store}
	ctx := serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive)
	purl := "pkg:golang/github.com/jackc/pgx/v5@v5.10.0"

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for _, symbol := range []string{"", "Batch", "CollectRows", "ParseConfig"} {
		wg.Add(1)
		go func(symbol string) {
			defer wg.Done()
			_, _, err := w.SnapshotJSONWithError(ctx, purl, symbol)
			errs <- err
		}(symbol)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("bulk snapshot attempt did not start")
	}
	close(store.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err == nil {
			t.Fatal("coalesced failed load was converted to a cache miss")
		}
	}
	if got := store.bulkCalls.Load(); got != 1 {
		t.Fatalf("concurrent bulk calls = %d, want one coalesced attempt", got)
	}
	if got := store.singleCalls.Load(); got != 0 {
		t.Fatalf("singleton fallback calls = %d, want 0", got)
	}
}

func TestSnapshotSameLaneWaiterHonorsItsContext(t *testing.T) {
	store := &blockingBulkSnapshotStore{
		failingBulkSnapshotStore: &failingBulkSnapshotStore{Fake: serverstore.NewFake()},
		started:                  make(chan struct{}),
		release:                  make(chan struct{}),
	}
	w := &webStore{s: store}
	purl := "pkg:golang/github.com/jackc/pgx/v5@v5.10.0"
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		ctx := serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive)
		_, _, _ = w.SnapshotJSONWithError(ctx, purl, "")
	}()
	<-store.started

	waitCtx, cancel := context.WithTimeout(
		serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive),
		25*time.Millisecond,
	)
	defer cancel()
	started := time.Now()
	if _, _, err := w.SnapshotJSONWithError(waitCtx, purl, "Batch"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter error = %v, want its context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("context-cancelled waiter remained parked for %s", elapsed)
	}
	if got := store.bulkCalls.Load(); got != 1 {
		t.Fatalf("same-lane bulk calls = %d, want one", got)
	}
	close(store.release)
	<-leaderDone
}

type priorityBulkSnapshotStore struct {
	*serverstore.Fake
	backgroundStarted chan struct{}
	backgroundRelease chan struct{}
	backgroundOnce    sync.Once
	calls             atomic.Int64
}

func (s *priorityBulkSnapshotStore) GetSnapshotsForPURL(ctx context.Context, purl string) ([]serverstore.SnapshotRow, error) {
	s.calls.Add(1)
	if serverstore.QueryClassOf(ctx) == serverstore.ClassBackground {
		s.backgroundOnce.Do(func() { close(s.backgroundStarted) })
		select {
		case <-s.backgroundRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Fake.GetSnapshotsForPURL(ctx, purl)
}

func TestInteractiveSnapshotLoadDoesNotJoinBackgroundLeader(t *testing.T) {
	store := &priorityBulkSnapshotStore{
		Fake:              serverstore.NewFake(),
		backgroundStarted: make(chan struct{}),
		backgroundRelease: make(chan struct{}),
	}
	w := &webStore{s: store}
	purl := "pkg:golang/github.com/jackc/pgx/v5@v5.10.0"
	backgroundDone := make(chan struct{})
	go func() {
		defer close(backgroundDone)
		ctx := serverstore.WithQueryClass(context.Background(), serverstore.ClassBackground)
		_, _, _ = w.SnapshotJSONWithError(ctx, purl, "")
	}()
	<-store.backgroundStarted

	ctx, cancel := context.WithTimeout(
		serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive),
		250*time.Millisecond,
	)
	defer cancel()
	if _, _, err := w.SnapshotJSONWithError(ctx, purl, "Batch"); err != nil {
		t.Fatalf("interactive load waited for background work: %v", err)
	}
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("bulk calls = %d, want separate background and interactive leaders", got)
	}
	close(store.backgroundRelease)
	<-backgroundDone
}

type routeSnapshotCountingStore struct {
	*serverstore.Fake
	bulkCalls   atomic.Int64
	singleCalls atomic.Int64
}

func (s *routeSnapshotCountingStore) GetSnapshotsForPURL(ctx context.Context, purl string) ([]serverstore.SnapshotRow, error) {
	s.bulkCalls.Add(1)
	return s.Fake.GetSnapshotsForPURL(ctx, purl)
}

func (s *routeSnapshotCountingStore) GetSnapshot(ctx context.Context, purl, symbol string) (string, bool, error) {
	s.singleCalls.Add(1)
	return s.Fake.GetSnapshot(ctx, purl, symbol)
}

func TestVersionPackageRouteBoundsSnapshotDBAcquisitions(t *testing.T) {
	ctx := t.Context()
	fake := serverstore.NewFake()
	purl := "pkg:golang/github.com/jackc/pgx/v5@v5.10.0"
	if err := fake.UpsertPackage(ctx, serverstore.PackageRow{
		PURL: purl, Ecosystem: "golang", Name: "github.com/jackc/pgx/v5", Version: "v5.10.0",
		Publicness: "PUBLIC", FirstSeen: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, symbol := range []string{"", "Batch"} {
		if err := fake.PutSnapshot(ctx, purl, symbol, `{"purl":"pkg:golang/github.com/jackc/pgx/v5@v5.10.0","rows":[]}`); err != nil {
			t.Fatal(err)
		}
	}
	store := &routeSnapshotCountingStore{Fake: fake}
	mux := http.NewServeMux()
	web.Register(mux, web.Deps{Store: &webStore{s: store}, PublicURL: "https://codesamplex.com"})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet,
			"https://codesamplex.com/golang/github.com/jackc/pgx/v5/v5.10.0", nil)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, body=%s", i+1, res.Code, res.Body.String())
		}
	}
	if got := store.bulkCalls.Load(); got != 1 {
		t.Fatalf("version route bulk snapshot DB calls = %d, want 1", got)
	}
	if got := store.singleCalls.Load(); got != 0 {
		t.Fatalf("version route singleton snapshot DB calls = %d, want 0", got)
	}
}

func TestVersionPackageRouteKeepsSnapshotPressureAs503DuringBackoff(t *testing.T) {
	ctx := t.Context()
	fake := serverstore.NewFake()
	purl := "pkg:golang/github.com/jackc/pgx/v5@v5.10.0"
	if err := fake.PutSnapshot(ctx, purl, "", `{"purl":"pkg:golang/github.com/jackc/pgx/v5@v5.10.0","rows":[]}`); err != nil {
		t.Fatal(err)
	}
	store := &failingBulkSnapshotStore{Fake: fake}
	mux := http.NewServeMux()
	web.Register(mux, web.Deps{Store: &webStore{s: store}, PublicURL: "https://codesamplex.com"})

	for attempt := 1; attempt <= 2; attempt++ {
		req := httptest.NewRequest(http.MethodGet,
			"https://codesamplex.com/golang/github.com/jackc/pgx/v5/v5.10.0", nil)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != http.StatusServiceUnavailable {
			t.Fatalf("request %d status = %d, want 503; body=%s", attempt, res.Code, res.Body.String())
		}
	}
	if got := store.bulkCalls.Load(); got != 1 {
		t.Fatalf("bulk calls = %d, want one attempt followed by retry backoff", got)
	}
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
