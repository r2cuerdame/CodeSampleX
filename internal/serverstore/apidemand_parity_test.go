package serverstore

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/apidemand"
)

// apiDemandScript is one scripted week of demand: two routes, three
// outcomes, every auth class, two client builds, two countries, an
// observation older than the report window and one older than retention.
func apiDemandScript(now time.Time) []apidemand.Batch {
	obs := func(at time.Time, route, outcome, auth, caller, country, kind, version, protocol string, latency int64) apidemand.Observation {
		return apidemand.Observation{At: at, Route: route, Outcome: outcome, Auth: auth, CallerHash: caller, Country: country, ClientKind: kind, ClientVersion: version, Protocol: protocol, LatencyMS: latency}
	}
	first := apidemand.NewBatch()
	first.Add(obs(now.Add(-30*time.Minute), "POST /v2/search", apidemand.OutcomeSuccess, apidemand.AuthAnonymous, "a", "KR", apidemand.ClientMCP, "v0.1.190", "2025-06-18", 42))
	first.Add(obs(now.Add(-30*time.Minute), "POST /v2/search", apidemand.OutcomeSuccess, apidemand.AuthAnonymous, "a", "KR", apidemand.ClientMCP, "v0.1.190", "2025-06-18", 7))
	first.Add(obs(now.Add(-2*time.Hour), "POST /v2/search", apidemand.OutcomeFailure, apidemand.AuthAuthenticated, "b", "US", apidemand.ClientCLI, "v0.1.195", "", 6000))
	first.Add(obs(now.Add(-3*24*time.Hour), "GET /v1/stats", apidemand.OutcomeRejected, apidemand.AuthUnidentified, "", "", apidemand.ClientNone, "", "", 1))
	second := apidemand.NewBatch()
	second.Add(obs(now.Add(-30*time.Minute), "POST /v2/search", apidemand.OutcomeSuccess, apidemand.AuthAnonymous, "c", "KR", apidemand.ClientMCP, "v0.1.190", "2025-06-18", 300))
	second.Add(obs(now.Add(-9*24*time.Hour), "GET /v1/stats", apidemand.OutcomeSuccess, apidemand.AuthAnonymous, "d", "DE", apidemand.ClientDaemon, "v0.1.180", "", 12))
	second.Add(obs(now.Add(-20*24*time.Hour), "GET /v1/stats", apidemand.OutcomeSuccess, apidemand.AuthAnonymous, "e", "DE", apidemand.ClientDaemon, "v0.1.170", "", 12))
	second.Add(obs(now.Add(-40*24*time.Hour), "GET /v1/stats", apidemand.OutcomeSuccess, apidemand.AuthAnonymous, "f", "FR", apidemand.ClientDaemon, "v0.1.160", "", 12))
	return []apidemand.Batch{first, second}
}

func playAPIDemand(t *testing.T, store apidemand.Store, now time.Time) (apidemand.Report, apidemand.Report) {
	t.Helper()
	ctx := context.Background()
	for _, batch := range apiDemandScript(now) {
		if err := store.UpsertDemand(ctx, batch); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	before, err := store.DemandReport(ctx, now)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := store.PruneDemand(ctx, now.Add(-apidemand.DefaultRetention)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	after, err := store.DemandReport(ctx, now)
	if err != nil {
		t.Fatalf("report after prune: %v", err)
	}
	return before, after
}

func TestFakeAPIDemandReportShape(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 30, 0, 0, time.UTC)
	before, after := playAPIDemand(t, NewFake(), now)
	if len(before.Hours) != 4 || before.UniqueCallers != 3 || before.PriorUniqueCallers != 1 {
		t.Fatalf("report = %+v", before)
	}
	if len(before.Routes) != 3 || before.Routes[1].Route != "POST /v2/search" || before.Routes[2].Requests != 3 || before.Routes[2].Buckets[2] != 1 || before.Routes[2].Buckets[0] != 1 {
		t.Fatalf("routes = %+v", before.Routes)
	}
	if len(before.Clients) != 3 || len(before.Countries) != 3 {
		t.Fatalf("clients=%+v countries=%+v", before.Clients, before.Countries)
	}
	if !before.OldestHour.Equal(now.Add(-40 * 24 * time.Hour).Truncate(time.Hour)) {
		t.Fatalf("oldest = %v", before.OldestHour)
	}
	if !after.OldestHour.Equal(now.Add(-20 * 24 * time.Hour).Truncate(time.Hour)) {
		t.Fatalf("oldest after prune = %v", after.OldestHour)
	}
}

func TestIntegrationAPIDemandParity(t *testing.T) {
	pg := openTestPG(t)
	now := time.Date(2026, 9, 17, 12, 30, 0, 0, time.UTC)
	fakeBefore, fakeAfter := playAPIDemand(t, NewFake(), now)
	pgBefore, pgAfter := playAPIDemand(t, pg, now)
	if !reflect.DeepEqual(fakeBefore, pgBefore) {
		t.Fatalf("report parity\nfake=%+v\n pg=%+v", fakeBefore, pgBefore)
	}
	if !reflect.DeepEqual(fakeAfter, pgAfter) {
		t.Fatalf("report parity after prune\nfake=%+v\n pg=%+v", fakeAfter, pgAfter)
	}
	// Replaying the same batches adds exactly once more: the upsert path
	// merges counts, it never replaces them.
	ctx := context.Background()
	for _, batch := range apiDemandScript(now) {
		if err := pg.UpsertDemand(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	doubled, err := pg.DemandReport(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if doubled.Routes[2].Requests != 2*pgAfter.Routes[2].Requests || doubled.UniqueCallers != pgAfter.UniqueCallers {
		t.Fatalf("replay: %+v", doubled.Routes)
	}
}
