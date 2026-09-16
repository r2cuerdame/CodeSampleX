package admin

// CSX-453: Farm ingest observability. The admin farm panel answers "is
// evidence landing" (LastFarmIngestAt, server-side truth over evidence_agg)
// beside the live ClassFarmIngest pool counters CSX-461 already collects.
// Farm's own local queue-depth signal (health-report.json, PR #134, Farm
// repo) is NOT duplicated here -- this panel only ever answers what has
// actually landed.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// fakePoolStatsReader lets a test hand the admin handler a fixed PoolStats
// answer without a real connection pool behind it.
type fakePoolStatsReader struct {
	stats serverstore.PoolStats
}

func (f fakePoolStatsReader) PoolStats() serverstore.PoolStats { return f.stats }

func farmIngestPoolStats() serverstore.PoolStats {
	return serverstore.PoolStats{
		Enabled: true, MaxConns: 12, Open: 12, InUse: 2, Idle: 10,
		Classes: []serverstore.ClassPoolStats{
			{Class: "interactive", Limit: 6, InUse: 0, Acquired: 900},
			{Class: "background", Limit: 4, InUse: 0, Acquired: 300},
			{
				Class: serverstore.ClassFarmIngest.String(), Limit: 1, InUse: 1,
				Attempts: 42, Acquired: 40, Waited: 5, WaitMax: 800 * time.Millisecond,
				Busy: 2, Timeouts: 1,
			},
		},
	}
}

// The panel must surface the farm_ingest class specifically -- not just
// whatever happens to be first in the pool table -- and it must pair the
// pool counters with the server's own "when did evidence last land" signal.
func TestFarmIngestPanelReportsLastCommitAndFarmIngestPoolClass(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }

	if _, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-09-16", AnonID: "farm-peer", ProjectBucket: "farm-proj",
		Package: "pkg:npm/axios@1.12.0", Symbol: "axios.post", SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22.18",
		},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
	}}); err != nil || len(rejected) != 0 {
		t.Fatalf("seed ingest: rejected=%v err=%v", rejected, err)
	}

	secret := "a-long-random-admin-secret"
	mux := http.NewServeMux()
	if !Register(mux, Deps{
		Store: &fakeStore{}, TokenSHA256: digest(secret), PublicURL: "https://codesamplex.dev",
		Version: "v1.2.3-test", StartedAt: now.Add(-time.Hour),
		Now: func() time.Time { return now }, Farm: store,
		PoolStats: fakePoolStatsReader{stats: farmIngestPoolStats()},
	}) {
		t.Fatal("valid token hash did not register /admin")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		FarmIngest struct {
			LastIngestAt string `json:"lastIngestAt"`
			CheckedAt    string `json:"checkedAt"`
			Pool         struct {
				Limit    int    `json:"limit"`
				InUse    int    `json:"inUse"`
				Attempts uint64 `json:"attempts"`
				Acquired uint64 `json:"acquired"`
				Waited   uint64 `json:"waited"`
				WaitMax  string `json:"waitMax"`
				Busy     uint64 `json:"busy"`
				Timeouts uint64 `json:"timeouts"`
			} `json:"pool"`
		} `json:"farmIngest"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if want := now.UTC().Format(time.RFC3339); payload.FarmIngest.LastIngestAt != want {
		t.Fatalf("farmIngest.lastIngestAt = %q, want %q", payload.FarmIngest.LastIngestAt, want)
	}
	if payload.FarmIngest.CheckedAt == "" {
		t.Fatal("farmIngest.checkedAt empty after a successful read")
	}
	pool := payload.FarmIngest.Pool
	if pool.Limit != 1 || pool.InUse != 1 || pool.Attempts != 42 || pool.Acquired != 40 ||
		pool.Waited != 5 || pool.Busy != 2 || pool.Timeouts != 1 {
		t.Fatalf("farmIngest.pool = %+v, want the farm_ingest class row, not the whole pool table", pool)
	}
	if pool.WaitMax != "800 ms" {
		t.Fatalf("farmIngest.pool.waitMax = %q, want %q", pool.WaitMax, "800 ms")
	}
}

// Before any batch has ever been accepted, the panel must say so plainly
// rather than rendering an empty string that could also mean "not measured".
func TestFarmIngestPanelBeforeAnyEvidenceHasLanded(t *testing.T) {
	store := serverstore.NewFake()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	secret := "a-long-random-admin-secret"
	mux := http.NewServeMux()
	if !Register(mux, Deps{
		Store: &fakeStore{}, TokenSHA256: digest(secret), PublicURL: "https://codesamplex.dev",
		Version: "v1.2.3-test", StartedAt: now.Add(-time.Hour),
		Now: func() time.Time { return now }, Farm: store,
	}) {
		t.Fatal("valid token hash did not register /admin")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		FarmIngest struct {
			LastIngestAt string          `json:"lastIngestAt"`
			CheckedAt    string          `json:"checkedAt"`
			Pool         json.RawMessage `json:"pool"`
		} `json:"farmIngest"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.FarmIngest.LastIngestAt != "" {
		t.Fatalf("lastIngestAt = %q, want empty before any batch has ever been accepted", payload.FarmIngest.LastIngestAt)
	}
	if payload.FarmIngest.CheckedAt == "" {
		t.Fatal("checkedAt empty: a real (found=false) read still happened and should be timestamped")
	}
	if payload.FarmIngest.Pool != nil {
		t.Fatalf("pool = %s, want omitted without a configured PoolStats reader", payload.FarmIngest.Pool)
	}
}

// ingestMemoStore lets a test drive LastFarmIngestAt's success/found/error
// outcomes independently of the rest of Fake's behavior, mirroring
// coverageMemoStore in farm_coverage_memo_test.go.
type ingestMemoStore struct {
	*serverstore.Fake
	value time.Time
	found bool
	err   error
	calls int
}

func (s *ingestMemoStore) LastFarmIngestAt(ctx context.Context) (time.Time, bool, error) {
	s.calls++
	if err := ctx.Err(); err != nil {
		return time.Time{}, false, err
	}
	if s.err != nil {
		return time.Time{}, false, s.err
	}
	return s.value, s.found, nil
}

// A LastFarmIngestAt failure must serve the memo's last-known value -- the
// same last-known-good/backoff contract farmCoverageMemo already holds --
// never crash or error the whole admin page.
func TestFarmIngestMemoKeepsLastGoodThroughTTLAndFailureDeferral(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	initial := now.Add(-5 * time.Minute)
	fresh := now.Add(-time.Minute)
	store := &ingestMemoStore{Fake: serverstore.NewFake(), value: initial, found: true}
	h := &handler{farmStats: store, now: func() time.Time { return now }}
	read := func(wantValue, wantAt time.Time, wantCalls int) {
		t.Helper()
		got, at := h.lastFarmIngestAt(t.Context(), now)
		if !got.Equal(wantValue) || !at.Equal(wantAt) || store.calls != wantCalls {
			t.Fatalf("value=%s at=%s calls=%d, want value=%s at=%s calls=%d",
				got, at, store.calls, wantValue, wantAt, wantCalls)
		}
	}
	firstAt := now
	read(initial, firstAt, 1)

	store.value = fresh
	now = now.Add(farmIngestTTL - time.Second)
	read(initial, firstAt, 1) // still inside TTL: no new call

	now = now.Add(2 * time.Second)
	store.err = errors.New("database query failed")
	read(initial, firstAt, 2) // TTL expired, read attempted, failed: last-good served

	deferredUntil := now.Add(farmIngestBackoff)
	if !h.farmIngest.retryAt.Equal(deferredUntil) {
		t.Fatalf("failure defer = %s, want %s", h.farmIngest.retryAt, deferredUntil)
	}

	store.err = nil
	now = deferredUntil.Add(-time.Second)
	read(initial, firstAt, 2) // still inside backoff: no retry yet

	now = deferredUntil
	read(fresh, now, 3) // backoff elapsed: fresh value served
	if !h.farmIngest.retryAt.IsZero() {
		t.Fatal("successful recovery retained the failure backoff")
	}
}

// found=false (no batch has ever been accepted) is a real answer, not an
// error: no retry backoff, and the read's own completion time still
// advances so the memo does not hammer the store every poll.
func TestFarmIngestNotYetLandedIsNotAnError(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	store := &ingestMemoStore{Fake: serverstore.NewFake()}
	h := &handler{farmStats: store, now: func() time.Time { return now }}
	value, at := h.lastFarmIngestAt(t.Context(), now)
	if !value.IsZero() {
		t.Fatalf("value = %s, want zero before any evidence has landed", value)
	}
	if !at.Equal(now) {
		t.Fatalf("at = %s, want %s (a real read happened)", at, now)
	}
	if !h.farmIngest.retryAt.IsZero() {
		t.Fatal("not-yet-landed installed a failure backoff")
	}
}
