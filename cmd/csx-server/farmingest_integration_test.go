package main

// End-to-end proof of CSX-453: CodeSampleX-Farm's own admission floor means
// saturating Farm's evidence-ingest traffic cannot exhaust the connections
// public interactive reads need, the way csx-451's separate Builder pool
// proves the same property for the aggregation pipeline. Both live in this
// package because both need the real HTTP handler, a real PostgreSQL pool
// and the real dbClassFor routing -- not fakes -- to prove a class boundary
// that only exists at that layer.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// blockEvidenceAgg holds the lock that makes every evidence-batch ingest
// wait on its INSERT ... ON CONFLICT into evidence_agg, and returns the
// release. Modeled on blockWanted (dbpressure_integration_test.go).
func blockEvidenceAgg(t *testing.T, conn *pgx.Conn) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := conn.Exec(ctx, "LOCK TABLE evidence_agg IN ACCESS EXCLUSIVE MODE"); err != nil {
		_, _ = conn.Exec(ctx, "ROLLBACK")
		t.Fatalf("lock evidence_agg: %v", err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if _, err := conn.Exec(context.Background(), "ROLLBACK"); err != nil {
			t.Errorf("release lock: %v", err)
		}
	}
	t.Cleanup(release)
	return release
}

func postEvidenceBatch(t *testing.T, url string, anonID string) (status int, err error) {
	t.Helper()
	batch := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-13", AnonID: anonID, ProjectBucket: "proj1",
		Package: "pkg:npm/axios@1.12.0",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
	}
	payload, merr := json.Marshal(map[string]any{"batches": []domain.ObservationBatch{batch}})
	if merr != nil {
		t.Fatalf("marshal: %v", merr)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, perr := client.Post(url+"/v1/evidence/batches", "application/json", bytes.NewReader(payload))
	if perr != nil {
		return 0, perr
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// TestIntegrationSaturatedFarmIngestDoesNotStarveInteractiveReads is the
// CSX-453 counterpart of TestIntegrationBuilderPoolSaturationDoesNotStarveWebAPI
// (#451): drive every connection ClassFarmIngest may ever hold to blocked,
// real work, and prove public interactive routes stay fast and healthy
// throughout.
func TestIntegrationSaturatedFarmIngestDoesNotStarveInteractiveReads(t *testing.T) {
	pol := testServerPoolPolicy()
	srv, outside, pg := openTestServer(t, pol)
	release := blockEvidenceAgg(t, outside)

	inFlight := pol.FarmIngestConns + 2
	var wg sync.WaitGroup
	for i := 0; i < inFlight; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = postEvidenceBatch(t, srv.URL, "peer-saturate")
		}()
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if classStatOf(pg.PoolStats(), "farm_ingest").InUse >= pol.FarmIngestConns {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if inUse := classStatOf(pg.PoolStats(), "farm_ingest").InUse; inUse < pol.FarmIngestConns {
		t.Fatalf("farm_ingest class never saturated: InUse=%d, want >= %d", inUse, pol.FarmIngestConns)
	}

	before := classStatOf(pg.PoolStats(), "interactive")
	client := &http.Client{Timeout: 5 * time.Second}
	const rounds = 5
	for round := 0; round < rounds; round++ {
		for _, path := range []string{"/healthz", "/v1/wanted"} {
			started := time.Now()
			resp, err := client.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("round %d %s: %v", round, path, err)
			}
			resp.Body.Close()
			if resp.StatusCode >= 500 {
				t.Errorf("round %d %s answered %d while farm_ingest was saturated", round, path, resp.StatusCode)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Errorf("round %d %s took %v while farm_ingest was saturated, want <=1s (SLO)", round, path, elapsed.Round(time.Millisecond))
			}
		}
	}
	after := classStatOf(pg.PoolStats(), "interactive")
	if after.Busy != before.Busy || after.Timeouts != before.Timeouts {
		t.Fatalf("interactive class absorbed pressure from farm_ingest: before=%+v after=%+v", before, after)
	}

	release()
	wg.Wait()
}

// TestIntegrationSaturatedFarmIngestGets503NotAHang proves the backpressure
// half of CSX-453: once farm_ingest's own floor is exhausted, a further
// evidence-batch request is refused with the same ErrPoolBusy/503 contract
// public reads already have, inside FarmIngestWait -- not left to hang
// until the HTTP client's own timeout, which is what an unbounded class
// (ClassBackground, pre-CSX-453's classification of this route) would do.
func TestIntegrationSaturatedFarmIngestGets503NotAHang(t *testing.T) {
	pol := testServerPoolPolicy()
	pol.FarmIngestConns = 1
	pol.FarmIngestWait = 300 * time.Millisecond
	srv, outside, pg := openTestServer(t, pol)
	release := blockEvidenceAgg(t, outside)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = postEvidenceBatch(t, srv.URL, "peer-hold")
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if classStatOf(pg.PoolStats(), "farm_ingest").InUse >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if inUse := classStatOf(pg.PoolStats(), "farm_ingest").InUse; inUse < 1 {
		t.Fatal("farm_ingest never took its one connection")
	}

	started := time.Now()
	status, err := postEvidenceBatch(t, srv.URL, "peer-refused")
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("second batch: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (ErrPoolBusy)", status)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("refusal took %v, want well under the HTTP client timeout -- FarmIngestWait=%v should have decided this", elapsed, pol.FarmIngestWait)
	}

	release()
	wg.Wait()
}
