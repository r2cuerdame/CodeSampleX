package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/apidemand"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Every registered route is labelled with its registration pattern, the
// status the client saw is the outcome, and the anonymous middleware's own
// hash is the caller. A request that never matches a route is not demand.
func TestRoutesRecordDemandTelemetryUnderTheirPattern(t *testing.T) {
	var collector *apidemand.Collector
	var store *serverstore.Fake
	srv, store, _ := newTestServer(t, func(d *Deps) {
		collector = apidemand.New(context.Background(), d.Store.(apidemand.Store), apidemand.Config{
			CountryHeader: "X-Edge-Country", FlushEvery: time.Hour, Now: d.Now,
		})
		d.Demand = collector
	})
	client := srv.Client()
	do := func(method, path string, headers map[string]string) int {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	anon := strings.Repeat("ef", 32)
	do("GET", "/v1/stats", map[string]string{"User-Agent": "csx/v0.1.190 (mcp; protocol=2025-06-18)", "X-Edge-Country": "kr", "X-CSX-Anonymous-ID": anon})
	do("GET", "/v1/stats", map[string]string{"User-Agent": "csx/v0.1.190 (mcp; protocol=2025-06-18)", "X-Edge-Country": "kr", "X-CSX-Anonymous-ID": anon})
	if status := do("GET", "/v1/samples/not-a-sample", map[string]string{"User-Agent": "curl/8"}); status < 400 {
		t.Fatalf("unknown sample = %d", status)
	}
	do("GET", "/nope", nil)
	if err := collector.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := store.DemandReport(context.Background(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	byRoute := map[string]apidemand.RouteRow{}
	for _, row := range report.Routes {
		byRoute[row.Route+"|"+row.Outcome] = row
	}
	if row := byRoute["GET /v1/stats|"+apidemand.OutcomeSuccess]; row.Requests != 2 {
		t.Fatalf("stats row = %+v (all: %+v)", row, report.Routes)
	}
	if row := byRoute["GET /v1/samples/{sampleId}|"+apidemand.OutcomeRejected]; row.Requests != 1 {
		t.Fatalf("sample row = %+v (all: %+v)", row, report.Routes)
	}
	if len(byRoute) != 2 {
		t.Fatalf("unmatched paths must not be recorded: %+v", report.Routes)
	}
	if report.UniqueCallers != 1 || len(report.RouteCallers) != 1 || report.RouteCallers[0].Route != "GET /v1/stats" {
		t.Fatalf("callers = %d %+v", report.UniqueCallers, report.RouteCallers)
	}
	for _, row := range report.Hours {
		if row.Outcome == apidemand.OutcomeSuccess && row.Auth != apidemand.AuthAnonymous {
			t.Fatalf("stats requests carried a valid anonymous id: %+v", row)
		}
	}
	clients := map[string]int64{}
	for _, row := range report.Clients {
		clients[row.Kind+"|"+row.Version+"|"+row.Protocol] = row.Requests
	}
	if clients["mcp|v0.1.190|2025-06-18"] != 2 || clients["other||"] != 1 {
		t.Fatalf("clients = %+v", report.Clients)
	}
	countries := map[string]int64{}
	for _, row := range report.Countries {
		countries[row.Country] = row.Requests
	}
	if countries["KR"] != 2 || countries[""] != 1 {
		t.Fatalf("countries = %+v", report.Countries)
	}
}
