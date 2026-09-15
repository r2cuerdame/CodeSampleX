package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type farmBudgetStore struct {
	*serverstore.Fake
	mu       sync.Mutex
	calls    []string
	failRead string
	waitRead string
}

func (s *farmBudgetStore) read(ctx context.Context, name string) error {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	s.mu.Unlock()
	if name == s.waitRead {
		<-ctx.Done()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == s.failRead {
		return errors.New("farm read unavailable")
	}
	return nil
}

func (s *farmBudgetStore) callsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *farmBudgetStore) FarmWorkers(ctx context.Context, _, _ time.Time) ([]serverstore.FarmWorker, error) {
	return []serverstore.FarmWorker{{Label: "author-1", Drafts: 5}}, s.read(ctx, "workers")
}

func (s *farmBudgetStore) FarmHealthNow(ctx context.Context, _ time.Time) (serverstore.FarmHealth, error) {
	return serverstore.FarmHealth{PublicSamples: 11}, s.read(ctx, "health")
}

func (s *farmBudgetStore) FarmBacklogNow(ctx context.Context, _, _ time.Time) (serverstore.FarmBacklog, error) {
	return serverstore.FarmBacklog{CoverageHoles: 7, FirstProven: 2}, s.read(ctx, "backlog")
}

func (s *farmBudgetStore) FarmCompletenessNow(ctx context.Context) (serverstore.FarmCompleteness, error) {
	return serverstore.FarmCompleteness{DependencyUnknown: 4}, s.read(ctx, "completeness")
}

func (s *farmBudgetStore) FarmCoverage(ctx context.Context) ([]serverstore.FarmAxisCoverage, error) {
	return nil, s.read(ctx, "coverage")
}

// Coverage can serve its last computed value on timeout. Spending the shared
// request budget on that optional refresh first used to make an otherwise
// readable backlog fail with the coverage query's expired context.
func TestFarmPanelRequiredReadsSurviveCoverageDeadline(t *testing.T) {
	for _, cache := range []string{"cold", "stale"} {
		t.Run(cache, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now().UTC()
				store := &farmBudgetStore{Fake: serverstore.NewFake(), waitRead: "coverage"}
				secret := "a-long-random-admin-secret"
				h := &handler{
					farmStats: store, farmGate: make(chan struct{}, 1),
					now: time.Now, wantHash: sha256.Sum256([]byte(secret)),
				}
				var wantCoverage []serverstore.FarmAxisCoverage
				var wantAt time.Time
				if cache == "stale" {
					wantCoverage = []serverstore.FarmAxisCoverage{{OS: "linux", Ecosystem: "golang", Proven: 3}}
					wantAt = now.Add(-2 * farmCoverageTTL)
					h.farmCoverage.value, h.farmCoverage.at = wantCoverage, wantAt
				}
				// Fake time expires a shorter caller budget without a wall-clock
				// delay or changing the route's production timeout.
				ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
				defer cancel()
				req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/admin/api/farm", nil)
				req.SetBasicAuth("recuerdame", secret)
				rec := httptest.NewRecorder()
				h.farm(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 after required reads; calls=%v body=%s", rec.Code, store.callsSnapshot(), rec.Body.String())
				}
				gotCalls := store.callsSnapshot()
				if len(gotCalls) != 5 || gotCalls[4] != "coverage" {
					t.Fatalf("reads = %v, want four required reads followed by coverage", gotCalls)
				}
				sort.Strings(gotCalls[:4])
				wantCalls := []string{"backlog", "completeness", "health", "workers"}
				if !reflect.DeepEqual(gotCalls[:4], wantCalls) {
					t.Fatalf("required reads = %v, want %v", gotCalls[:4], wantCalls)
				}
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					t.Fatalf("coverage did not exhaust the shared caller budget: %v", ctx.Err())
				}
				var payload struct {
					Workers []struct{ Drafts int }
					Health  struct{ PublicSamples int }
					Backlog struct {
						CoverageHoles       int
						FirstProvenInWindow int
					}
					Completeness struct{ DependencyUnknown int }
					Coverage     []struct {
						OS        string
						Ecosystem string
						Proven    int
					}
					CoverageAt string
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload.Workers) != 1 || payload.Workers[0].Drafts != 5 || payload.Health.PublicSamples != 11 ||
					payload.Backlog.CoverageHoles != 7 || payload.Backlog.FirstProvenInWindow != 2 || payload.Completeness.DependencyUnknown != 4 {
					t.Fatalf("required measurements were lost: %+v", payload)
				}
				if len(payload.Coverage) != len(wantCoverage) || payload.CoverageAt != adminTimeOrEmpty(wantAt) {
					t.Fatalf("coverage fallback changed: value=%v at=%q", payload.Coverage, payload.CoverageAt)
				}
				if cache == "stale" && (payload.Coverage[0].OS != "linux" || payload.Coverage[0].Ecosystem != "golang" || payload.Coverage[0].Proven != 3) {
					t.Fatalf("last-good coverage changed: %+v", payload.Coverage)
				}
				if !reflect.DeepEqual(h.farmCoverage.value, wantCoverage) || !h.farmCoverage.at.Equal(wantAt) || !h.farmCoverage.retryAt.IsZero() {
					t.Fatal("caller timeout changed the last-good coverage, its age, or shared backoff")
				}
			})
		})
	}
}

// Optional fallback must never turn a failed required measurement into a
// successful snapshot, even if the store returns partial values with its error.
func TestFarmPanelRequiredReadFailureSkipsOptionalCoverage(t *testing.T) {
	stages := []struct {
		name string
		body string
	}{
		{"workers", "팜 워커를 불러오지 못했습니다\n"},
		{"health", "팜 상태를 불러오지 못했습니다\n"},
		{"backlog", "백로그를 불러오지 못했습니다\n"},
		{"completeness", "완성도 집계를 불러오지 못했습니다\n"},
	}
	for _, stage := range stages {
		for _, failure := range []string{"error", "deadline"} {
			t.Run(stage.name+"/"+failure, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					store := &farmBudgetStore{Fake: serverstore.NewFake(), failRead: stage.name}
					if failure == "deadline" {
						store.waitRead = stage.name
					}
					mux, secret := farmMux(t, store, nil)
					ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
					defer cancel()
					req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/admin/api/farm", nil)
					req.SetBasicAuth("recuerdame", secret)
					rec := httptest.NewRecorder()
					mux.ServeHTTP(rec, req)
					if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != stage.body {
						t.Fatalf("failed required read returned status=%d body=%q", rec.Code, rec.Body.String())
					}
					gotCalls := store.callsSnapshot()
					sort.Strings(gotCalls)
					wantCalls := []string{"backlog", "completeness", "health", "workers"}
					if !reflect.DeepEqual(gotCalls, wantCalls) {
						t.Fatalf("reads = %v, want all bounded required reads and no optional coverage", gotCalls)
					}
				})
			})
		}
	}
}

type boundedFarmStore struct {
	*serverstore.Fake
	inFlight atomic.Int64
	max      atomic.Int64
}

func (s *boundedFarmStore) wait(ctx context.Context) error {
	inFlight := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		max := s.max.Load()
		if inFlight <= max || s.max.CompareAndSwap(max, inFlight) {
			break
		}
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *boundedFarmStore) FarmWorkers(ctx context.Context, _, _ time.Time) ([]serverstore.FarmWorker, error) {
	return nil, s.wait(ctx)
}

func (s *boundedFarmStore) FarmHealthNow(ctx context.Context, _ time.Time) (serverstore.FarmHealth, error) {
	return serverstore.FarmHealth{}, s.wait(ctx)
}

func (s *boundedFarmStore) FarmBacklogNow(ctx context.Context, _, _ time.Time) (serverstore.FarmBacklog, error) {
	return serverstore.FarmBacklog{}, s.wait(ctx)
}

func (s *boundedFarmStore) FarmCompletenessNow(ctx context.Context) (serverstore.FarmCompleteness, error) {
	return serverstore.FarmCompleteness{}, s.wait(ctx)
}

// The four required snapshots used to consume their latencies serially. Keep
// the overlap below the background pool's four-connection cap: two independent
// reads at a time cut the critical path in half without letting one admin poll
// occupy the entire background lane.
func TestFarmPanelRequiredReadsUseTwoBoundedWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		secret := "a-long-random-admin-secret"
		store := &boundedFarmStore{Fake: serverstore.NewFake()}
		h := &handler{
			farmStats: store, farmGate: make(chan struct{}, 1), now: time.Now,
			wantHash: sha256.Sum256([]byte(secret)),
		}
		req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
		req.SetBasicAuth("recuerdame", secret)
		rec := httptest.NewRecorder()
		started := time.Now()
		h.farm(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if elapsed := time.Since(started); elapsed != 2*time.Second {
			t.Fatalf("required-read critical path = %s, want 2s for two waves", elapsed)
		}
		if max := store.max.Load(); max != 2 {
			t.Fatalf("required-read concurrency = %d, want exactly 2", max)
		}
	})
}
