package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type pressureAwareSnapshotStore struct {
	*fakeStore
	mu       sync.Mutex
	failPURL string
	reads    []string
}

func (s *pressureAwareSnapshotStore) SnapshotJSONWithError(
	ctx context.Context, purl, symbol string,
) (string, bool, error) {
	s.mu.Lock()
	s.reads = append(s.reads, snapKey(purl, symbol))
	s.mu.Unlock()
	if s.failPURL == "" || purl == s.failPURL {
		return "", false, errors.New("scripted pool refusal")
	}
	raw, ok := s.fakeStore.SnapshotJSON(ctx, purl, symbol)
	return raw, ok, nil
}

func (s *pressureAwareSnapshotStore) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reads)
}

func TestPackageRouteStopsAfterFirstSnapshotPressure(t *testing.T) {
	store := &pressureAwareSnapshotStore{fakeStore: newCubeStore()}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	res := get(t, mux, "/npm/reactish")
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want unavailable; body=%s", res.Code, res.Body.String())
	}
	if got := store.readCount(); got != 1 {
		t.Fatalf("snapshot reads after first pressure refusal = %d, want 1", got)
	}
}

func TestPinnedRepairPressureIsUnavailableNotFalseNoMatch(t *testing.T) {
	store := &pressureAwareSnapshotStore{
		fakeStore: deepLinkStore(),
		failPURL:  "pkg:npm/semverish@6.3.1",
	}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	res := get(t, mux, "/npm/semverish?f_symbol=semver.clean&f_version=6.3.1")
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want unavailable; body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "No recorded evidence matches these filters") {
		t.Fatal("pool refusal was rendered as an authoritative empty coordinate")
	}
}
