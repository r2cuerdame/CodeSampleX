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
