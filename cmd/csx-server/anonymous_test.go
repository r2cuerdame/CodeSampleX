package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/anonymousclient"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestAnonymousProductionMuxCollectsOnlyProductTraffic(t *testing.T) {
	f := serverstore.NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux, _ := buildMuxWithTracker(ctx, serverstore.ServerConfig{PublicCheck: "trust"}, f)
	for _, path := range []string{"/v1/adapters", "/v1/adapters", "/healthz", "/version", "/v1/unknown-route"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set(anonymousclient.Header, strings.Repeat("a", 64))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if path == "/v1/adapters" && w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	m, err := f.AnonymousAnalytics(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if m.DAU != 1 || len(m.Daily) != 1 || m.Daily[0].Requests != 2 {
		t.Fatalf("wrong production collection: %+v", m)
	}
}
