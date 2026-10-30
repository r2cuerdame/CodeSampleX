package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestReadinessDetectsRepeatedAuthoringFailuresWithHealthyDatabase(t *testing.T) {
	clock := testNow
	store := &authoring503StageStore{Fake: serverstore.NewFake(), stage: "claim"}
	token := "csx_author_v1_" + strings.Repeat("YWFh", 10) + "YWE"
	authoringSession(t, store.Fake, token, "readiness-writer", clock)
	mux := NewMux(Deps{Store: store, Now: func() time.Time { return clock }})
	request := func(method, path, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	ready := func(want int) {
		t.Helper()
		w := request(http.MethodGet, "/readyz", "")
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("readiness = %d %q headers=%v, want %d and no-store", w.Code, w.Body.String(), w.Header(), want)
		}
	}
	ready(http.StatusOK)
	for range 3 {
		if w := request(http.MethodPost, "/v1/authoring/work/next", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated work poll = %d", w.Code)
		}
	}
	ready(http.StatusOK)
	for i := range 3 {
		if w := request(http.MethodPost, "/v1/authoring/work/next", token); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("failed work poll = %d", w.Code)
		}
		if i == 0 {
			ready(http.StatusOK)
		}
	}
	ready(http.StatusServiceUnavailable)
	for range 3 {
		request(http.MethodPost, "/v1/authoring/work/next", "")
	}
	ready(http.StatusServiceUnavailable)
	if w := request(http.MethodGet, "/healthz", ""); w.Code != http.StatusOK {
		t.Fatalf("healthy database liveness = %d", w.Code)
	}
	store.stage = ""
	for range 4 {
		if w := request(http.MethodPost, "/v1/authoring/work/next", token); w.Code != http.StatusOK {
			t.Fatalf("successful work poll = %d %s", w.Code, w.Body.String())
		}
	}
	ready(http.StatusOK)
	store.stage = "claim"
	for range 3 {
		request(http.MethodPost, "/v1/authoring/work/next", token)
	}
	ready(http.StatusServiceUnavailable)
	clock = clock.Add(16 * time.Minute)
	ready(http.StatusOK)
}

func TestReadinessRequiresDatabase(t *testing.T) {
	w := httptest.NewRecorder()
	NewMux(Deps{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness without database = %d, want 503", w.Code)
	}
}
