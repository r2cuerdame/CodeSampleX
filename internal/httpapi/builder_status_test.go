package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
)

// #517: production on 2026-09-26T01:50Z. The rollup still said
// 2026-09-17T12:34:46Z and the last pass had hit its ceiling. GET /v1/builder
// must call that stale and say why, without a host key or an admin token.
func TestBuilderStatusReportsTheProductionStallAsStale(t *testing.T) {
	srv, store, ck := newTestServer(t, nil)
	ck.t = time.Date(2026, 9, 26, 1, 50, 0, 0, time.UTC)
	ctx := context.Background()
	if err := store.SetStatsDaily(ctx, "2026-09-17", `{"generatedAt":"2026-09-17T12:34:46Z"}`); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBuilderStatus(ctx, compatibility.BuilderStatusName, `{
		"lastPassStartedAt":"2026-09-25T19:40:00Z","lastPassFinishedAt":"2026-09-26T01:40:00Z",
		"lastPassOutcome":"timeout","lastPassFull":true,
		"lastSuccessAt":"2026-09-17T14:02:11Z","lastSuccessGeneratedAt":"2026-09-17T12:34:46Z",
		"lastFailureAt":"2026-09-26T01:40:00Z","lastFailureReason":"timeout",
		"lastFailurePhase":"snapshot_write","lastFailureClass":"deadline","consecutiveFailures":34,
		"repair":{"startedAt":"2026-09-25T19:40:00Z","cursor":"npm/lodash","packagesDone":400,"packagesTotal":1603,"chunks":2,"chunkPackages":100}}`); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	resp := getJSON(t, srv.URL+"/v1/builder", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got["stale"] != true {
		t.Fatalf("an 8.5-day-old rollup was not reported stale: %v", got)
	}
	// 2026-09-17T12:34:46Z -> 2026-09-26T01:50:00Z
	if age, _ := got["statsAgeSeconds"].(float64); int64(age) != 738914 {
		t.Errorf("statsAgeSeconds = %v, want 738914 (8.55 days)", got["statsAgeSeconds"])
	}
	if got["statsGeneratedAt"] != "2026-09-17T12:34:46Z" || got["lastSuccessAt"] != "2026-09-17T14:02:11Z" {
		t.Errorf("stamps = %v / %v", got["statsGeneratedAt"], got["lastSuccessAt"])
	}
	if got["lastFailureReason"] != "timeout" || got["lastFailurePhase"] != "snapshot_write" {
		t.Errorf("failure = %v in %v", got["lastFailureReason"], got["lastFailurePhase"])
	}
	if repair, _ := got["repair"].(map[string]any); repair == nil || repair["cursor"] != "npm/lodash" {
		t.Errorf("repair progress missing: %v", got["repair"])
	}
	if got["recorded"] != true || got["staleAfterSeconds"] != float64(86400) {
		t.Errorf("recorded=%v staleAfterSeconds=%v", got["recorded"], got["staleAfterSeconds"])
	}
}

func TestBuilderStatusIsFreshInsideADay(t *testing.T) {
	srv, store, ck := newTestServer(t, nil)
	ck.t = time.Date(2026, 9, 26, 1, 50, 0, 0, time.UTC)
	if err := store.SetStatsDaily(context.Background(), "2026-09-26", `{"generatedAt":"2026-09-26T01:45:00Z"}`); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if resp := getJSON(t, srv.URL+"/v1/builder", &got); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got["stale"] != false || got["recorded"] != false {
		t.Fatalf("five-minute-old rollup with no record = stale %v recorded %v", got["stale"], got["recorded"])
	}
}

// Nothing has ever finished: there is no clock to show, which is stale.
func TestBuilderStatusWithNoRollupIsStale(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	var got map[string]any
	if resp := getJSON(t, srv.URL+"/v1/builder", &got); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got["stale"] != true {
		t.Fatalf("no rollup at all was reported fresh: %v", got)
	}
}
