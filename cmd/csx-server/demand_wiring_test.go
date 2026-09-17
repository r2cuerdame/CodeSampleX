package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/anonymousclient"
	"github.com/r2cuerdame/codesamplex/internal/apidemand"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The production mux wires the demand collector to the store, labels API
// routes by their registration pattern, leaves the health check and the
// website out, and the admin page renders what was collected.
func TestProductionMuxRecordsAPIDemandAndRendersThePanel(t *testing.T) {
	f := serverstore.NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	secret := "an-admin-secret-for-the-demand-test"
	digest := sha256.Sum256([]byte(secret))
	cfg := serverstore.ServerConfig{PublicCheck: "trust", AdminTokenSHA256: hex.EncodeToString(digest[:]), CountryHeader: "X-Edge-Country"}
	mux, _, collector := buildMuxWithTrackerAndWanted(ctx, cfg, f, nil)
	if !collector.Available() || !collector.CountryEnabled() {
		t.Fatalf("collector not wired: available=%v country=%v", collector.Available(), collector.CountryEnabled())
	}
	for _, path := range []string{"/v1/adapters", "/v1/adapters", "/healthz", "/", "/v1/unknown-route"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set(anonymousclient.Header, strings.Repeat("a", 64))
		r.Header.Set("User-Agent", "csx/v0.1.100 (cli)")
		r.Header.Set("X-Edge-Country", "KR")
		mux.ServeHTTP(httptest.NewRecorder(), r)
	}
	if err := collector.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := f.DemandReport(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Routes) != 1 || report.Routes[0].Route != "GET /v1/adapters" || report.Routes[0].Requests != 2 || report.Routes[0].Outcome != apidemand.OutcomeSuccess {
		t.Fatalf("routes = %+v", report.Routes)
	}
	if len(report.Countries) != 1 || report.Countries[0].Country != "KR" || report.Countries[0].Requests != 2 {
		t.Fatalf("countries = %+v", report.Countries)
	}
	if len(report.Clients) != 1 || report.Clients[0].Kind != apidemand.ClientCLI || report.Clients[0].Version != "v0.1.100" {
		t.Fatalf("clients = %+v", report.Clients)
	}

	// A second mux over the same store renders the page from the rows the
	// first one flushed; the collector is closed, so this one only reads.
	mux, _, second := buildMuxWithTrackerAndWanted(ctx, cfg, f, nil)
	defer second.Close(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.SetBasicAuth("recuerdame", secret)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<section id="demand-diagnostics"`,
		`<td><code>GET /v1/adapters</code></td><td>2</td><td>1</td>`,
		`<td><code>KR</code></td><td>2</td><td>100.0%</td>`,
		`<td>CLI</td><td><code>v0.1.100</code></td><td>2</td>`,
		`패키지·라이브러리 수요 TOP`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin page is missing %q", want)
		}
	}
}
