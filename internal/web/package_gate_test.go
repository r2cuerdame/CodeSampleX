package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// navigationPressureStore makes two package-version reads slow while leaving
// the already-warm package untouched. A page-wide semaphore cannot tell these
// cases apart; the cache-miss admission in the production adapter can.
type navigationPressureStore struct {
	*fakeStore
	entered chan string
	unblock chan struct{}
}

func (s *navigationPressureStore) PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error) {
	if name == "slow-one" || name == "slow-two" {
		s.entered <- name
		select {
		case <-s.unblock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.fakeStore.PackageVersions(ctx, ecosystem, name)
}

func TestWarmPackageNavigationBypassesUnrelatedSlowPages(t *testing.T) {
	store := &navigationPressureStore{
		fakeStore: newFakeStore(),
		entered:   make(chan string, 2),
		unblock:   make(chan struct{}),
	}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	type result struct {
		rec  *httptest.ResponseRecorder
		done chan struct{}
	}
	start := func(path string) *result {
		res := &result{rec: httptest.NewRecorder(), done: make(chan struct{})}
		go func() {
			defer close(res.done)
			mux.ServeHTTP(res.rec, httptest.NewRequest(http.MethodGet, path, nil))
		}()
		return res
	}

	slowOne := start("/npm/slow-one")
	slowTwo := start("/npm/slow-two")
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-store.entered:
			seen[name] = true
		case <-time.After(2 * time.Second):
			t.Fatal("two slow package pages did not reach their underlying reads")
		}
	}

	started := time.Now()
	warm := get(t, mux, "/npm/axios")
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("warm package navigation waited %v behind unrelated pages", elapsed)
	}
	if warm.Code != http.StatusOK {
		t.Fatalf("warm package navigation status = %d, want 200", warm.Code)
	}

	select {
	case <-slowOne.done:
		t.Fatal("first slow page completed before its store read was released")
	default:
	}
	select {
	case <-slowTwo.done:
		t.Fatal("second slow page completed before its store read was released")
	default:
	}

	close(store.unblock)
	for _, res := range []*result{slowOne, slowTwo} {
		select {
		case <-res.done:
		case <-time.After(2 * time.Second):
			t.Fatal("slow page did not drain")
		}
	}
}

// concurrentColdStore verifies that required versions finish before the four
// optional reads start. This avoids self-rejection at the four-slot admission gate.
type concurrentColdStore struct {
	versionsRelease chan struct{}
	*fakeStore
	entered chan string
	unblock chan struct{}
}

func (s *concurrentColdStore) wait(ctx context.Context, name string) error {
	s.entered <- name
	unblock := s.unblock
	if name == "versions" {
		unblock = s.versionsRelease
	}
	select {
	case <-unblock:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *concurrentColdStore) PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error) {
	if err := s.wait(ctx, "versions"); err != nil {
		return nil, err
	}
	return s.fakeStore.PackageVersions(ctx, ecosystem, name)
}

func (s *concurrentColdStore) PackageSamples(ctx context.Context, ecosystem, name string, limit int) ([]SampleListItem, error) {
	if err := s.wait(ctx, "samples"); err != nil {
		return nil, err
	}
	return s.fakeStore.PackageSamples(ctx, ecosystem, name, limit)
}

func (s *concurrentColdStore) PackageCodeCounts(ctx context.Context, ecosystem, name string) ([]PackageCodeCount, error) {
	if err := s.wait(ctx, "counts"); err != nil {
		return nil, err
	}
	return s.fakeStore.PackageCodeCounts(ctx, ecosystem, name)
}

func (s *concurrentColdStore) WantedForPackage(ctx context.Context, ecosystem, name string) ([]WantedRow, error) {
	if err := s.wait(ctx, "wanted"); err != nil {
		return nil, err
	}
	return s.fakeStore.WantedForPackage(ctx, ecosystem, name)
}

func (s *concurrentColdStore) FailureClusters(ctx context.Context, ecosystem, name string) ([]string, int, error) {
	if err := s.wait(ctx, "clusters"); err != nil {
		return nil, 0, err
	}
	return s.fakeStore.FailureClusters(ctx, ecosystem, name)
}

func TestPackagePagePrioritizesVersionsBeforeFourColdReads(t *testing.T) {
	store := &concurrentColdStore{
		versionsRelease: make(chan struct{}),
		fakeStore:       newFakeStore(),
		entered:         make(chan string, 5),
		unblock:         make(chan struct{}),
	}
	t.Cleanup(func() {
		for _, ch := range []chan struct{}{store.versionsRelease, store.unblock} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
	})
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/npm/axios", nil))
	}()

	select {
	case name := <-store.entered:
		if name != "versions" {
			t.Fatalf("optional read %s started before required versions", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("versions did not start")
	}
	select {
	case name := <-store.entered:
		t.Fatalf("read %s competed with required versions", name)
	default:
	}
	close(store.versionsRelease)
	seen := map[string]bool{}
	for len(seen) < 4 {
		select {
		case name := <-store.entered:
			seen[name] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("cold reads remained serialized; entered %v", seen)
		}
	}
	close(store.unblock)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("package page did not finish after cold reads were released")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("package page status = %d, want 200", rec.Code)
	}
}

type panicPackageSamplesStore struct{ *fakeStore }

func (s *panicPackageSamplesStore) PackageSamples(context.Context, string, string, int) ([]SampleListItem, error) {
	panic("test store panic")
}
func TestPackageOptionalReadPanicDoesNotCrashServer(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = &panicPackageSamplesStore{newFakeStore()} })
	rec := get(t, mux, "/npm/axios")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want usable page despite optional read panic", rec.Code)
	}
}
