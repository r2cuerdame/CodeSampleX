package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type blockingAuthoringCandidates struct {
	*serverstore.Fake
	calls    atomic.Int64
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
	once     sync.Once
}

func newBlockingAuthoringCandidates() *blockingAuthoringCandidates {
	return &blockingAuthoringCandidates{
		Fake: serverstore.NewFake(), started: make(chan struct{}, 1),
		release: make(chan struct{}), finished: make(chan struct{}),
	}
}

func (s *blockingAuthoringCandidates) ListAuthoringExpansionCandidates(ctx context.Context, _ int) ([]serverstore.WantedRow, error) {
	s.calls.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	defer s.once.Do(func() { close(s.finished) })
	select {
	case <-s.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestConcurrentAuthoringPollsShareOneCandidateScan(t *testing.T) {
	store := newBlockingAuthoringCandidates()
	a := &api{d: Deps{Store: store, authoringWorkTimeout: time.Second}}
	const callers = 8
	start := make(chan struct{})
	entered := make(chan struct{}, callers)
	results := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			entered <- struct{}{}
			_, err := a.loadAuthoringCandidates(context.Background(), store)
			results <- err
		}()
	}
	close(start)
	for range callers {
		<-entered
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("candidate scan did not start")
	}
	// Give every released caller a chance to join the deliberately blocked
	// call. The assertion is made before the store is released, so a second
	// scan cannot hide behind a quick completion.
	time.Sleep(25 * time.Millisecond)
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("concurrent candidate scans = %d, want 1", got)
	}
	close(store.release)
	for range callers {
		if err := <-results; err != nil {
			t.Fatalf("shared candidate scan: %v", err)
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("candidate scans after every caller returned = %d, want 1", got)
	}
}

func TestAuthoringPollTimesOutBeforeClientAndEndsTheScan(t *testing.T) {
	store := newBlockingAuthoringCandidates()
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store.Fake, token, "bounded-writer", testNow)
	deps := Deps{
		Store:                store,
		Cfg:                  serverstore.ServerConfig{PublicCheck: "trust", Publishing: "open"},
		Now:                  func() time.Time { return testNow },
		authoringWorkTimeout: 40 * time.Millisecond,
	}
	srv := httptest.NewServer(NewMux(deps))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next",
		bytes.NewBufferString(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	started := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("authoring timeout status = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("server-owned authoring timeout took %v", elapsed)
	}
	select {
	case <-store.finished:
	case <-time.After(time.Second):
		t.Fatal("HTTP timeout returned but the candidate scan remained alive")
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("candidate scans = %d, want 1", got)
	}
}

func TestAuthoringCandidateScanPreservesThePollAbsoluteDeadline(t *testing.T) {
	store := newBlockingAuthoringCandidates()
	a := &api{d: Deps{Store: store, authoringWorkTimeout: time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// Model session refresh consuming most of the poll. Candidate discovery
	// may detach from a disconnected caller for joined workers, but it must
	// not receive a new full second here.
	time.Sleep(60 * time.Millisecond)
	started := time.Now()
	_, err := a.loadAuthoringCandidates(ctx, store)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("candidate scan error = %v, want original poll deadline", err)
	}
	if elapsed := time.Since(started); elapsed >= 250*time.Millisecond {
		t.Fatalf("candidate scan replaced the remaining absolute deadline: %v", elapsed)
	}
	select {
	case <-store.finished:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("candidate scan survived the poll's absolute deadline")
	}
}

type authoringCandidateRetryCall struct {
	unhurried bool
	hasBudget bool
	class     serverstore.QueryClass
}

type retryingAuthoringCandidates struct {
	*serverstore.Fake
	mu     sync.Mutex
	calls  []authoringCandidateRetryCall
	failAt map[int]bool
	rows   []serverstore.WantedRow
}

func newRetryingAuthoringCandidates(failAt ...int) *retryingAuthoringCandidates {
	failures := make(map[int]bool, len(failAt))
	for _, call := range failAt {
		failures[call] = true
	}
	return &retryingAuthoringCandidates{
		Fake: serverstore.NewFake(), failAt: failures,
		rows: []serverstore.WantedRow{{
			Ecosystem: "npm", Name: "retry-success", Version: "1.0.0",
			Symbol: "run", Kind: "EXPANSION",
		}},
	}
}

func (s *retryingAuthoringCandidates) read(ctx context.Context, unhurried bool) ([]serverstore.WantedRow, error) {
	budget := serverstore.BudgetOf(ctx)
	call := authoringCandidateRetryCall{unhurried: unhurried, hasBudget: budget != nil}
	if budget != nil {
		call.class = budget.Class()
	}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	n := len(s.calls)
	fail := s.failAt[n]
	rows := append([]serverstore.WantedRow(nil), s.rows...)
	s.mu.Unlock()
	if fail {
		return nil, errBrokenExpansion
	}
	return rows, nil
}

func (s *retryingAuthoringCandidates) ListAuthoringExpansionCandidates(ctx context.Context, _ int) ([]serverstore.WantedRow, error) {
	return s.read(ctx, false)
}

func (s *retryingAuthoringCandidates) ListAuthoringExpansionCandidatesUnhurried(ctx context.Context, _ int) ([]serverstore.WantedRow, error) {
	return s.read(ctx, true)
}

func (s *retryingAuthoringCandidates) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *retryingAuthoringCandidates) recordedCalls() []authoringCandidateRetryCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authoringCandidateRetryCall(nil), s.calls...)
}

func TestAuthoringCandidateFailureRetriesFiveTimesThenDefersForTheTTL(t *testing.T) {
	store := newRetryingAuthoringCandidates(1, 2, 3, 4, 5, 6)
	clock := atomic.Int64{}
	clock.Store(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC).UnixNano())
	delays := make(chan time.Duration, retrypolicy.MaxRetries)
	a := &api{d: Deps{
		Store: store,
		Now: func() time.Time {
			return time.Unix(0, clock.Load()).UTC()
		},
		authoringWorkTimeout: time.Second,
	}}
	a.authoringCandidates.retryDraw = func(time.Duration) time.Duration { return 0 }
	a.authoringCandidates.retryWait = func(delay time.Duration) { delays <- delay }

	if _, err := a.loadAuthoringCandidates(context.Background(), store); !errors.Is(err, errBrokenExpansion) {
		t.Fatalf("first candidate scan error = %v, want %v", err, errBrokenExpansion)
	}
	waitFor(t, func() bool { return store.callCount() == 1+retrypolicy.MaxRetries },
		"candidate retry series did not reach its bounded terminal attempt")
	waitFor(t, func() bool {
		a.authoringCandidates.mu.Lock()
		defer a.authoringCandidates.mu.Unlock()
		return a.authoringCandidates.retries.State() == retrypolicy.FailedDeferred
	}, "candidate retry series did not enter failed/deferred")

	wantDelays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	for i, want := range wantDelays {
		select {
		case got := <-delays:
			if got != want {
				t.Fatalf("retry delay %d = %v, want %v", i+1, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("retry delay %d was not scheduled", i+1)
		}
	}

	calls := store.recordedCalls()
	if calls[0].unhurried {
		t.Fatal("the request-owned first scan used the background expansion path")
	}
	for i, call := range calls[1:] {
		if !call.unhurried || !call.hasBudget || call.class != serverstore.ClassBackground {
			t.Fatalf("retry %d = %+v, want unhurried scan with a fresh background budget", i+1, call)
		}
	}

	// The terminal state is durable for the same normal window used by a
	// successful snapshot. A poll inside it receives the last failure and does
	// not silently open attempt seven.
	if _, err := a.loadAuthoringCandidates(context.Background(), store); !errors.Is(err, errBrokenExpansion) {
		t.Fatalf("deferred poll error = %v, want last scan failure", err)
	}
	if got := store.callCount(); got != 1+retrypolicy.MaxRetries {
		t.Fatalf("failed/deferred poll restarted the scan: calls=%d", got)
	}

	clock.Add(int64(authoringCandidateTTL))
	snap, err := a.loadAuthoringCandidates(context.Background(), store)
	if err != nil {
		t.Fatalf("fresh series after the deferred TTL: %v", err)
	}
	if got := store.callCount(); got != 2+retrypolicy.MaxRetries {
		t.Fatalf("fresh series call count = %d, want %d", got, 2+retrypolicy.MaxRetries)
	}
	if len(snap.expansion) != 1 || snap.expansion[0].Name != "retry-success" {
		t.Fatalf("fresh series snapshot = %+v", snap.expansion)
	}
}

func TestAuthoringCandidatePollsDoNotRestartAScanDuringRetryBackoff(t *testing.T) {
	store := newRetryingAuthoringCandidates(1)
	retryScheduled := make(chan time.Duration, 1)
	releaseRetry := make(chan struct{})
	a := &api{d: Deps{Store: store, authoringWorkTimeout: time.Second}}
	a.authoringCandidates.retryDraw = func(time.Duration) time.Duration { return 0 }
	a.authoringCandidates.retryWait = func(delay time.Duration) {
		retryScheduled <- delay
		<-releaseRetry
	}

	if _, err := a.loadAuthoringCandidates(context.Background(), store); !errors.Is(err, errBrokenExpansion) {
		t.Fatalf("first candidate scan error = %v, want %v", err, errBrokenExpansion)
	}
	select {
	case got := <-retryScheduled:
		if got != time.Second {
			t.Fatalf("first retry delay = %v, want 1s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate retry was not scheduled")
	}

	started := time.Now()
	if _, err := a.loadAuthoringCandidates(context.Background(), store); !errors.Is(err, errBrokenExpansion) {
		t.Fatalf("poll during backoff error = %v, want last scan failure", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("poll during backoff waited %v instead of returning the retained failure", elapsed)
	}
	if got := store.callCount(); got != 1 {
		t.Fatalf("poll during backoff started another scan: calls=%d", got)
	}

	close(releaseRetry)
	waitFor(t, func() bool {
		a.authoringCandidates.mu.Lock()
		defer a.authoringCandidates.mu.Unlock()
		return a.authoringCandidates.have
	}, "the scheduled background retry did not publish its successful snapshot")
	if got := store.callCount(); got != 2 {
		t.Fatalf("background retry calls = %d, want one initial and one retry", got)
	}
}

func TestFailedAuthoringCandidateRefreshKeepsServingStaleWhileRetryWaits(t *testing.T) {
	store := newRetryingAuthoringCandidates(2)
	clock := atomic.Int64{}
	clock.Store(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC).UnixNano())
	retryScheduled := make(chan time.Duration, 1)
	releaseRetry := make(chan struct{})
	a := &api{d: Deps{
		Store: store,
		Now: func() time.Time {
			return time.Unix(0, clock.Load()).UTC()
		},
		authoringWorkTimeout: time.Second,
	}}
	a.authoringCandidates.retryDraw = func(time.Duration) time.Duration { return 0 }
	a.authoringCandidates.retryWait = func(delay time.Duration) {
		retryScheduled <- delay
		<-releaseRetry
	}

	first, err := a.loadAuthoringCandidates(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.expansion) != 1 {
		t.Fatalf("initial snapshot = %+v", first.expansion)
	}
	store.mu.Lock()
	store.rows[0].Name = "refreshed"
	store.mu.Unlock()
	clock.Add(int64(authoringCandidateTTL + time.Second))

	stale, err := a.loadAuthoringCandidates(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.expansion) != 1 || stale.expansion[0].Name != "retry-success" {
		t.Fatalf("stale poll served %+v", stale.expansion)
	}
	waitFor(t, func() bool { return store.callCount() == 2 }, "stale refresh did not run")
	select {
	case got := <-retryScheduled:
		if got != time.Second {
			t.Fatalf("refresh retry delay = %v, want 1s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("failed refresh did not schedule a retry")
	}

	again, err := a.loadAuthoringCandidates(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.expansion) != 1 || again.expansion[0].Name != "retry-success" {
		t.Fatalf("poll during refresh backoff served %+v", again.expansion)
	}
	if got := store.callCount(); got != 2 {
		t.Fatalf("poll during refresh backoff started another scan: calls=%d", got)
	}

	close(releaseRetry)
	waitFor(t, func() bool {
		snap, loadErr := a.loadAuthoringCandidates(context.Background(), store)
		return loadErr == nil && len(snap.expansion) == 1 && snap.expansion[0].Name == "refreshed"
	}, "successful refresh retry did not replace the stale snapshot")
	calls := store.recordedCalls()
	for i, call := range calls[1:] {
		if !call.unhurried || !call.hasBudget || call.class != serverstore.ClassBackground {
			t.Fatalf("background refresh call %d = %+v", i+1, call)
		}
	}
}
