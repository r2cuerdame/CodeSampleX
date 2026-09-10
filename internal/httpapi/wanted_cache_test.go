package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type controlledWantedStore struct {
	serverstore.Store

	mu        sync.Mutex
	calls     int
	classes   []serverstore.QueryClass
	rows      []serverstore.WantedRow
	err       error
	entered   chan struct{}
	release   chan struct{}
	completed chan struct{}
}

func waitWantedRefresh(t *testing.T, a *api) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.wantedMu.Lock()
		done := a.wantedRefresh == nil
		a.wantedMu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("wanted refresh did not finish")
}

func TestWantedEveryTTLRefreshStaysOffInteractivePool(t *testing.T) {
	now := time.Now().UTC()
	store := &controlledWantedStore{Store: serverstore.NewFake(), rows: []serverstore.WantedRow{{Name: "real-row"}}}
	a := &api{d: Deps{Store: store, Now: func() time.Time { return now }}, wantedAt: now.Add(-time.Minute), wantedItems: wantedListItems(store.rows)}
	for round := 0; round < 3; round++ {
		w := httptest.NewRecorder()
		a.handleWantedList(w, httptest.NewRequest(http.MethodGet, "/v1/wanted", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("round %d status %d", round, w.Code)
		}
		waitWantedRefresh(t, a)
		a.wantedMu.Lock()
		a.wantedAt = now.Add(-time.Minute)
		a.wantedMu.Unlock()
	}
	calls, classes := store.state()
	if calls != 3 {
		t.Fatalf("calls=%d, want three separate TTL refreshes", calls)
	}
	for _, class := range classes {
		if class != serverstore.ClassBackground {
			t.Fatalf("TTL refresh used %v pool", class)
		}
	}
}

func TestWantedRefreshHasFiveBackoffsThenTerminalDefer(t *testing.T) {
	now := time.Now().UTC()
	store := &controlledWantedStore{Store: serverstore.NewFake(), err: serverstore.ErrPoolBusy}
	a := &api{d: Deps{Store: store, Now: func() time.Time { return now }}, wantedAt: now.Add(-time.Minute), wantedItems: []wantedListItem{{Name: "last-good"}}}
	request := func() {
		w := httptest.NewRecorder()
		a.handleWantedList(w, httptest.NewRequest(http.MethodGet, "/v1/wanted", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("stale response status %d", w.Code)
		}
		waitWantedRefresh(t, a)
	}
	for attempt := 0; attempt <= retrypolicy.MaxRetries; attempt++ {
		request()
		a.wantedMu.Lock()
		next, state := a.wantedRetryAt, a.wantedRetry.State()
		a.wantedMu.Unlock()
		delay := next.Sub(now)
		if attempt < retrypolicy.MaxRetries {
			base := time.Second << attempt
			if state != retrypolicy.Waiting || delay < base || delay > base+base/4 {
				t.Fatalf("attempt %d state=%v delay=%v", attempt, state, delay)
			}
		} else if state != retrypolicy.FailedDeferred || delay != wantedRefreshDefer {
			t.Fatalf("terminal state=%v delay=%v", state, delay)
		}
		for i := 0; i < 10; i++ {
			request()
		}
		if calls, _ := store.state(); calls != attempt+1 {
			t.Fatalf("backoff issued %d calls after attempt %d", calls, attempt)
		}
		now = next
	}
	request()
	if calls, _ := store.state(); calls != 7 {
		t.Fatalf("new scheduling window calls=%d", calls)
	}
}

func TestWantedCanceledColdLeaderDoesNotPoisonWaiter(t *testing.T) {
	store := &controlledWantedStore{Store: serverstore.NewFake(), rows: []serverstore.WantedRow{{Name: "live"}}, entered: make(chan struct{}, 2), release: make(chan struct{})}
	a := &api{d: Deps{Store: store}}
	ctx, cancel := context.WithCancel(serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive))
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		a.handleWantedList(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/wanted", nil).WithContext(ctx))
	}()
	<-store.entered
	response := httptest.NewRecorder()
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		a.handleWantedList(response, httptest.NewRequest(http.MethodGet, "/v1/wanted", nil))
	}()
	cancel()
	<-leaderDone
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("canceled leader left the waiter in cached pressure")
	}
	close(store.release)
	select {
	case <-waiterDone:
	case <-time.After(time.Second):
		t.Fatal("waiter did not finish")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("waiter status=%d body=%s", response.Code, response.Body.String())
	}
}

func (s *controlledWantedStore) TopWanted(ctx context.Context, _ int) ([]serverstore.WantedRow, error) {
	s.mu.Lock()
	s.calls++
	s.classes = append(s.classes, serverstore.QueryClassOf(ctx))
	s.mu.Unlock()
	if s.entered != nil {
		s.entered <- struct{}{}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.completed != nil {
		defer func() { s.completed <- struct{}{} }()
	}
	return append([]serverstore.WantedRow(nil), s.rows...), s.err
}

func (s *controlledWantedStore) state() (int, []serverstore.QueryClass) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]serverstore.QueryClass(nil), s.classes...)
}

type wantedListResponse struct {
	SchemaVersion int              `json:"schemaVersion"`
	GeneratedAt   time.Time        `json:"generatedAt"`
	Items         []wantedListItem `json:"items"`
}

func fetchWanted(client *http.Client, url string) (wantedListResponse, int, time.Duration, error) {
	start := time.Now()
	resp, err := client.Get(url + "/v1/wanted")
	if err != nil {
		return wantedListResponse{}, 0, time.Since(start), err
	}
	defer resp.Body.Close()
	var body wantedListResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	return body, resp.StatusCode, time.Since(start), err
}

func TestWantedStaleSnapshotServesImmediatelyWithOneBackgroundRefresh(t *testing.T) {
	base := serverstore.NewFake()
	store := &controlledWantedStore{
		Store:   base,
		rows:    []serverstore.WantedRow{{Ecosystem: "npm", Name: "new", Version: "2.0.0", Asks: 2}},
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	seedAt := time.Now().Add(-time.Minute).UTC()
	srv := httptest.NewServer(NewMux(Deps{
		Store: store,
		Cfg:   serverstore.ServerConfig{PublicCheck: "trust"},
		WantedSnapshot: &WantedSnapshot{
			GeneratedAt: seedAt,
			Rows:        []serverstore.WantedRow{{Ecosystem: "npm", Name: "last-good", Version: "1.0.0", Asks: 1}},
		},
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	const readers = 12
	results := make(chan wantedListResponse, readers)
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, status, elapsed, err := fetchWanted(client, srv.URL)
			if err == nil && status != http.StatusOK {
				err = errors.New("wanted did not return HTTP 200")
			}
			if err == nil && elapsed > 500*time.Millisecond {
				err = errors.New("stale wanted response exceeded the latency bound")
			}
			if err != nil {
				errs <- err
				return
			}
			results <- body
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for body := range results {
		if body.SchemaVersion != 1 || len(body.Items) != 1 || body.Items[0].Name != "last-good" {
			t.Errorf("stale response = %+v", body)
		}
		if !body.GeneratedAt.Equal(seedAt) {
			t.Errorf("generatedAt = %v, want snapshot time %v", body.GeneratedAt, seedAt)
		}
	}

	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not start")
	}
	if calls, classes := store.state(); calls != 1 || len(classes) != 1 || classes[0] != serverstore.ClassBackground {
		t.Fatalf("refresh calls=%d classes=%v, want one background call", calls, classes)
	}
	close(store.release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		body, status, _, err := fetchWanted(client, srv.URL)
		if err == nil && status == http.StatusOK && len(body.Items) == 1 && body.Items[0].Name == "new" {
			if calls, _ := store.state(); calls != 1 {
				t.Fatalf("refresh call count = %d, want 1", calls)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("completed refresh was not published")
}

func TestWantedRefreshFailureKeepsLastGoodWithoutRetryStorm(t *testing.T) {
	now := time.Now().UTC()
	store := &controlledWantedStore{
		Store:     serverstore.NewFake(),
		err:       serverstore.ErrPoolBusy,
		completed: make(chan struct{}, 1),
	}
	srv := httptest.NewServer(NewMux(Deps{
		Store: store,
		Cfg:   serverstore.ServerConfig{PublicCheck: "trust"},
		Now:   func() time.Time { return now },
		WantedSnapshot: &WantedSnapshot{
			GeneratedAt: now.Add(-time.Minute),
			Rows:        []serverstore.WantedRow{{Ecosystem: "pypi", Name: "last-good", Version: "1.0.0"}},
		},
	}))
	defer srv.Close()

	client := &http.Client{Timeout: time.Second}
	if body, status, _, err := fetchWanted(client, srv.URL); err != nil || status != http.StatusOK || len(body.Items) != 1 {
		t.Fatalf("first stale response status=%d body=%+v err=%v", status, body, err)
	}
	select {
	case <-store.completed:
	case <-time.After(time.Second):
		t.Fatal("failed refresh did not complete")
	}
	for i := 0; i < 10; i++ {
		body, status, elapsed, err := fetchWanted(client, srv.URL)
		if err != nil || status != http.StatusOK || len(body.Items) != 1 || body.Items[0].Name != "last-good" {
			t.Fatalf("stale fallback %d status=%d body=%+v err=%v", i, status, body, err)
		}
		if elapsed > 500*time.Millisecond {
			t.Fatalf("stale fallback %d took %v", i, elapsed)
		}
	}
	if calls, _ := store.state(); calls != 1 {
		t.Fatalf("failed refresh caused %d attempts inside cooldown, want 1", calls)
	}
}

func TestWantedColdReadersShareOneLoad(t *testing.T) {
	store := &controlledWantedStore{
		Store:   serverstore.NewFake(),
		rows:    []serverstore.WantedRow{{Ecosystem: "cargo", Name: "singleflight", Version: "1.0.0"}},
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	srv := httptest.NewServer(NewMux(Deps{Store: store, Cfg: serverstore.ServerConfig{PublicCheck: "trust"}}))
	defer srv.Close()
	client := &http.Client{Timeout: 2 * time.Second}

	const readers = 12
	errCh := make(chan error, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, status, _, err := fetchWanted(client, srv.URL)
			if err == nil && (status != http.StatusOK || len(body.Items) != 1 || body.Items[0].Name != "singleflight") {
				err = errors.New("cold reader received the wrong wanted snapshot")
			}
			errCh <- err
		}()
	}
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("cold wanted load did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if calls, _ := store.state(); calls != 1 {
		t.Fatalf("concurrent cold readers started %d loads, want 1", calls)
	}
	close(store.release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Error(err)
		}
	}
}
