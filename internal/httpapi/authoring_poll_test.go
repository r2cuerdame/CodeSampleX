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
