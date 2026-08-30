package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type blockingFindingsStore struct {
	*fakeStore
	release        chan struct{}
	releaseOnce    sync.Once
	derivedStarted chan time.Duration
	handStarted    chan time.Duration
	derivedClass   chan serverstore.QueryClass
	handClass      chan serverstore.QueryClass
	derivedCalls   atomic.Int64
	manifestCalls  atomic.Int64
	derivedErr     error
}

type panickingFindingsStore struct {
	*fakeStore
	derivedCalls  atomic.Int64
	manifestCalls atomic.Int64
}

func (s *panickingFindingsStore) DerivedFindings(context.Context) ([]DerivedFinding, error) {
	s.derivedCalls.Add(1)
	panic("derived store panic")
}

func (s *panickingFindingsStore) SampleManifest(context.Context, string) (string, bool) {
	s.manifestCalls.Add(1)
	panic("manifest store panic")
}

func newBlockingFindingsStore() *blockingFindingsStore {
	return &blockingFindingsStore{
		fakeStore:      newFakeStore(),
		release:        make(chan struct{}),
		derivedStarted: make(chan time.Duration, 1),
		handStarted:    make(chan time.Duration, 1),
		derivedClass:   make(chan serverstore.QueryClass, 1),
		handClass:      make(chan serverstore.QueryClass, 1),
	}
}

func (s *blockingFindingsStore) unblock() { s.releaseOnce.Do(func() { close(s.release) }) }

func refreshDeadline(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	return time.Until(deadline)
}

func (s *blockingFindingsStore) DerivedFindings(ctx context.Context) ([]DerivedFinding, error) {
	s.derivedCalls.Add(1)
	select {
	case s.derivedStarted <- refreshDeadline(ctx):
	default:
	}
	select {
	case s.derivedClass <- serverstore.QueryClassOf(ctx):
	default:
	}
	if s.derivedErr != nil {
		return nil, s.derivedErr
	}
	select {
	case <-s.release:
		return s.fakeStore.DerivedFindings(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *blockingFindingsStore) SampleManifest(ctx context.Context, id string) (string, bool) {
	s.manifestCalls.Add(1)
	select {
	case s.handStarted <- refreshDeadline(ctx):
	default:
	}
	select {
	case s.handClass <- serverstore.QueryClassOf(ctx):
	default:
	}
	select {
	case <-s.release:
		return s.fakeStore.SampleManifest(ctx, id)
	case <-ctx.Done():
		return "", false
	}
}

func findingsMux(store Store) *http.ServeMux {
	mux := http.NewServeMux()
	Register(mux, Deps{Store: store, PublicURL: "https://codesamplex.dev", Build: testBuild()})
	return mux
}

func TestColdFindingsReturnsWhileBothCorpusRefreshesAreBlocked(t *testing.T) {
	store := newBlockingFindingsStore()
	t.Cleanup(store.unblock)
	store.derived = []DerivedFinding{{
		Ecosystem: "npm", Subject: "cold-only 1.0.0", Believed: "cold belief",
		Measured: "cold measurement", SampleID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	mux := findingsMux(store)

	started := time.Now()
	rec := get(t, mux, "/findings")
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("cold /findings waited for corpus refresh for %v", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("cold /findings status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "What the network found") || !strings.Contains(body, "deny_unknown_fields") {
		t.Fatal("cold /findings did not preserve the complete static page")
	}
	if strings.Contains(body, "cold belief") {
		t.Fatal("cold /findings rendered an incomplete derived refresh")
	}

	for name, startedCh := range map[string]<-chan time.Duration{
		"derived": store.derivedStarted,
		"hand":    store.handStarted,
	} {
		select {
		case remaining := <-startedCh:
			if remaining <= 0 || remaining > findingsRefreshTimeout+time.Second {
				t.Fatalf("%s refresh deadline remaining = %v", name, remaining)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s background refresh did not start", name)
		}
	}
	for name, classCh := range map[string]<-chan serverstore.QueryClass{
		"derived": store.derivedClass,
		"hand":    store.handClass,
	} {
		select {
		case class := <-classCh:
			if class != serverstore.ClassBackground {
				t.Fatalf("%s refresh class = %s, want background", name, class)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s refresh did not expose its DB class", name)
		}
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := get(t, mux, "/findings"); got.Code != http.StatusOK {
				t.Errorf("concurrent cold status = %d", got.Code)
			}
		}()
	}
	wg.Wait()
	if got := store.derivedCalls.Load(); got != 1 {
		t.Fatalf("concurrent derived refreshes = %d, want 1", got)
	}
	if got := store.manifestCalls.Load(); got != 1 {
		t.Fatalf("concurrent hand refreshes before release = %d, want 1", got)
	}

	store.unblock()
	deadline := time.Now().Add(time.Second)
	for {
		body = get(t, mux, "/findings").Body.String()
		if strings.Contains(body, "cold belief") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed derived refresh was not published")
		}
		time.Sleep(time.Millisecond)
	}
	if got := store.derivedCalls.Load(); got != 1 {
		t.Fatalf("fresh derived cache scheduled %d reads, want 1", got)
	}
}

func TestStaleFindingsReturnsLastCompleteSnapshot(t *testing.T) {
	store := newBlockingFindingsStore()
	t.Cleanup(store.unblock)
	s := &site{
		d:         Deps{Store: store, PublicURL: "https://codesamplex.dev", Build: testBuild()},
		tmpl:      parseTemplates(),
		derivedAt: time.Now().Add(-2 * derivedTTL),
		derivedCache: []finding{{
			Ecosystem: "npm", Subject: "last-good 1.0.0", Believed: "last good belief",
			Measured: "last good measurement", SampleID: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Basis: "sample", BasisKey: "findings.basis_sample",
		}},
		handAt:         time.Now(),
		handDocumented: baseHandFindings(documentedFindings, "docs", "findings.basis_docs"),
		handBelieved:   baseHandFindings(believedFindings, "belief", "findings.basis_belief"),
	}
	req := httptest.NewRequest(http.MethodGet, "https://codesamplex.dev/findings", nil)
	rec := httptest.NewRecorder()
	started := time.Now()
	s.findings(rec, req)
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("stale /findings waited for refresh for %v", elapsed)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "last good belief") {
		t.Fatalf("stale /findings did not serve last complete snapshot: status=%d", rec.Code)
	}
	select {
	case <-store.derivedStarted:
	case <-time.After(time.Second):
		t.Fatal("stale /findings did not start a refresh")
	}
}

func TestFailedFindingsRefreshUsesRetryCooldown(t *testing.T) {
	store := newBlockingFindingsStore()
	store.derivedErr = errors.New("database unavailable")
	s := &site{d: Deps{Store: store}}
	req := httptest.NewRequest(http.MethodGet, "https://codesamplex.dev/findings", nil)

	s.derivedFindings(req)
	deadline := time.Now().Add(time.Second)
	for {
		s.derivedMu.Lock()
		refreshing, retryAt := s.derivedRefreshing, s.derivedRetryAt
		s.derivedMu.Unlock()
		if !refreshing && retryAt.After(time.Now()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed refresh did not enter bounded retry cooldown")
		}
		time.Sleep(time.Millisecond)
	}
	for range 20 {
		s.derivedFindings(req)
	}
	if got := store.derivedCalls.Load(); got != 1 {
		t.Fatalf("refreshes during retry cooldown = %d, want 1", got)
	}
}

func TestFindingsRefreshPanicsPreserveLastGoodAndReleaseSingleflight(t *testing.T) {
	store := &panickingFindingsStore{fakeStore: newFakeStore()}
	s := &site{
		d:    Deps{Store: store, PublicURL: "https://codesamplex.dev", Build: testBuild()},
		tmpl: parseTemplates(),
		derivedCache: []finding{{
			Ecosystem: "npm", Subject: "panic-safe 1.0.0", Believed: "panic safe belief",
			Measured: "last good remains visible", SampleID: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			Basis: "sample", BasisKey: "findings.basis_sample",
		}},
		derivedAt:      time.Now().Add(-2 * derivedTTL),
		handDocumented: baseHandFindings(documentedFindings, "docs", "findings.basis_docs"),
		handBelieved:   baseHandFindings(believedFindings, "belief", "findings.basis_belief"),
		handAt:         time.Now().Add(-2 * derivedTTL),
	}
	req := httptest.NewRequest(http.MethodGet, "https://codesamplex.dev/findings", nil)
	rec := httptest.NewRecorder()
	s.findings(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "panic safe belief") {
		t.Fatalf("request did not survive refresh panics with last-good data: status=%d", rec.Code)
	}

	deadline := time.Now().Add(time.Second)
	for {
		s.derivedMu.Lock()
		derivedDone := !s.derivedRefreshing && s.derivedRetryAt.After(time.Now())
		derivedCache := append([]finding(nil), s.derivedCache...)
		s.derivedMu.Unlock()
		s.handMu.Lock()
		handDone := !s.handRefreshing && s.handRetryAt.After(time.Now())
		handDocumented := append([]finding(nil), s.handDocumented...)
		s.handMu.Unlock()
		if derivedDone && handDone {
			if len(derivedCache) != 1 || derivedCache[0].Believed != "panic safe belief" {
				t.Fatalf("derived panic replaced last-good cache: %+v", derivedCache)
			}
			if len(handDocumented) != len(documentedFindings) {
				t.Fatalf("hand panic replaced last-good cache: got %d rows", len(handDocumented))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("panic did not release both singleflight flags into cooldown")
		}
		time.Sleep(time.Millisecond)
	}

	for range 20 {
		s.findings(httptest.NewRecorder(), req)
	}
	if got := store.derivedCalls.Load(); got != 1 {
		t.Fatalf("derived panic retried during cooldown %d times, want 1", got)
	}
	if got := store.manifestCalls.Load(); got != 1 {
		t.Fatalf("hand panic retried during cooldown %d times, want 1", got)
	}
}

func TestFindingsRefreshTimeoutPreservesLastGood(t *testing.T) {
	store := newBlockingFindingsStore()
	t.Cleanup(store.unblock)
	s := &site{
		d:                 Deps{Store: store},
		derivedRefreshing: true,
		derivedCache: []finding{{
			Believed: "derived last good",
		}},
		handRefreshing: true,
		handDocumented: []finding{{
			Believed: "hand last good",
		}},
		handBelieved: []finding{},
	}

	go s.refreshDerivedFindingsWithin(10 * time.Millisecond)
	go s.refreshHandFindingsWithin(10 * time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for {
		s.derivedMu.Lock()
		derivedDone := !s.derivedRefreshing && s.derivedRetryAt.After(time.Now())
		derivedBelief := s.derivedCache[0].Believed
		s.derivedMu.Unlock()
		s.handMu.Lock()
		handDone := !s.handRefreshing && s.handRetryAt.After(time.Now())
		handBelief := s.handDocumented[0].Believed
		s.handMu.Unlock()
		if derivedDone && handDone {
			if derivedBelief != "derived last good" || handBelief != "hand last good" {
				t.Fatalf("timeout replaced last-good: derived=%q hand=%q", derivedBelief, handBelief)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bounded refreshes did not time out into cooldown")
		}
		time.Sleep(time.Millisecond)
	}
}
