package main

// The end-to-end shape of the R2C-55 outage, over real HTTP against a real
// PostgreSQL: one page's query cannot answer, many visitors ask for it at
// once, and the question is what happens to everyone else.
//
// The slow query is made slow the way production makes queries slow -- by
// another session holding a lock the read needs -- rather than by a sleep in
// the handler. That is the difference between testing the defense and
// testing the fixture: pg_sleep would never be cancelled by the same code
// path a blocked read is, and would never prove the connection comes back.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// testPoolPolicy is the shipped shape with the clock turned down, so the
// suite measures behaviour rather than patience.
func testServerPoolPolicy() serverstore.PoolPolicy {
	pol := serverstore.DefaultPoolPolicy()
	pol.ReadTimeout = 700 * time.Millisecond
	pol.ReadWait = 400 * time.Millisecond
	pol.ProbeTimeout = 700 * time.Millisecond
	pol.ProbeWait = time.Second
	return pol
}

// openTestServer brings up the whole csx-server handler on a throwaway
// schema of CSX_TEST_DSN, and returns it plus a second connection outside
// the pool for the lock the fixture needs.
func openTestServer(t *testing.T, pol serverstore.PoolPolicy) (*httptest.Server, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("CSX_TEST_DSN")
	if dsn == "" {
		if require := os.Getenv("CSX_REQUIRE_TEST_DSN"); require != "" {
			if off, err := strconv.ParseBool(require); err != nil || !off {
				t.Fatalf("CSX_TEST_DSN is empty while CSX_REQUIRE_TEST_DSN=%s demands the PostgreSQL suite", require)
			}
		}
		t.Skip("CSX_TEST_DSN is not set; skipping the PostgreSQL end-to-end pressure test")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("csx_srv_%d", time.Now().UnixNano())

	outside, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := outside.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		outside.Close(ctx)
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = outside.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		outside.Close(context.Background())
	})
	if _, err := outside.Exec(ctx, "SET search_path = "+schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	pg, err := serverstore.OpenWithPolicy(ctx, dsn+sep+"search_path="+schema, pol)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(pg.Close)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := serverstore.ServerConfig{
		PublicCheck: "trust",
		PublicURL:   "http://example.invalid",
		DBPool:      pol,
	}
	srv := httptest.NewServer(buildMux(ctx, cfg, pg))
	t.Cleanup(srv.Close)
	return srv, outside
}

// blockWanted holds the lock that makes every read of the request board
// wait, and returns the release.
func blockWanted(t *testing.T, conn *pgx.Conn) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := conn.Exec(ctx, "LOCK TABLE wanted IN ACCESS EXCLUSIVE MODE"); err != nil {
		_, _ = conn.Exec(ctx, "ROLLBACK")
		t.Fatalf("lock wanted: %v", err)
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

type result struct {
	status  int
	elapsed time.Duration
	err     error
}

func get(client *http.Client, url string) result {
	start := time.Now()
	resp, err := client.Get(url)
	if err != nil {
		return result{elapsed: time.Since(start), err: err}
	}
	defer resp.Body.Close()
	return result{status: resp.StatusCode, elapsed: time.Since(start)}
}

// TestIntegrationOneStuckPageDoesNotTakeTheSiteDown is R2C-58's completion
// condition, measured: while more visitors than the read class has
// connections are all stuck on /wanted, /healthz answers and so does another
// page that does not touch the blocked table.
func TestIntegrationOneStuckPageDoesNotTakeTheSiteDown(t *testing.T) {
	pol := testServerPoolPolicy()
	srv, outside := openTestServer(t, pol)
	release := blockWanted(t, outside)

	client := &http.Client{Timeout: 30 * time.Second}
	// Two more visitors than the read class is allowed to hold, so the last
	// arrivals are the ones that had nowhere to queue in the old pool.
	stuck := pol.InteractiveConns + 2
	results := make([]result, stuck)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = get(client, srv.URL+"/wanted")
		}(i)
	}
	// Sampled across the whole time the readers are stuck, not once: the
	// question is whether the site stays up for its duration, and a single
	// probe could land in the moment before the pool filled.
	for i := 0; i < 12; i++ {
		if health := get(client, srv.URL+"/healthz"); health.status != http.StatusOK {
			t.Fatalf("/healthz answered %d while /wanted was stuck; the container healthcheck would mark this instance unhealthy", health.status)
		} else if health.elapsed > 2*time.Second {
			t.Fatalf("/healthz took %v while /wanted was stuck", health.elapsed.Round(time.Millisecond))
		}
		// A page that does not read the blocked table must be unaffected.
		if features := get(client, srv.URL+"/features"); features.status >= 500 {
			t.Fatalf("/features answered %d while /wanted was stuck", features.status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	wg.Wait()

	// Nobody waited anywhere near the 60s WriteTimeout that turned this into
	// 502s, and everybody got an answer.
	worst := time.Duration(0)
	for i, r := range results {
		if r.err != nil {
			t.Errorf("visitor %d got no response at all: %v", i, r.err)
			continue
		}
		if r.status != http.StatusServiceUnavailable && r.status != http.StatusOK {
			t.Errorf("visitor %d got %d; a stuck read must be a 503 the visitor can retry", i, r.status)
		}
		worst = max(worst, r.elapsed)
	}
	if budget := pol.ReadWait + pol.ReadTimeout + 3*time.Second; worst > budget {
		t.Errorf("the slowest stuck visitor waited %v; the budgets allow %v", worst.Round(time.Millisecond), budget)
	}

	// And the page comes back on its own once the cause clears -- the
	// connections were returned, not burned.
	release()
	if r := get(client, srv.URL+"/wanted"); r.status != http.StatusOK {
		t.Fatalf("/wanted answered %d after the lock was released", r.status)
	}
}

// The API half of the same guarantee: a read that cannot be served because
// the database is under pressure is a 503 with a Retry-After, not a 500. A
// client that reads 500 has been told this server is broken.
func TestIntegrationBlockedAPIReadIsRetryableNotABug(t *testing.T) {
	pol := testServerPoolPolicy()
	srv, outside := openTestServer(t, pol)
	blockWanted(t, outside)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(srv.URL + "/v1/wanted")
	if err != nil {
		t.Fatalf("GET /v1/wanted: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/wanted answered %d while the table was locked, want 503", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("the 503 carries no Retry-After, so a client has nothing to back off on")
	}
}

// Ingest asked for no ceiling and must not have been given one: a write that
// waits on a lock longer than a page read would tolerate still completes.
func TestIntegrationIngestIsNotCutShortByTheReadCeiling(t *testing.T) {
	pol := testServerPoolPolicy()
	srv, outside := openTestServer(t, pol)

	// Hold the table for longer than a read would ever be allowed to wait,
	// then let go while the request is still in flight.
	release := blockWanted(t, outside)
	go func() {
		time.Sleep(pol.ReadTimeout + pol.ReadWait + 500*time.Millisecond)
		release()
	}()

	client := &http.Client{Timeout: 30 * time.Second}
	body := strings.NewReader(`{"reports":[{"package":"pkg:npm/left-pad@1.3.0","symbol":""}]}`)
	resp, err := client.Post(srv.URL+"/v1/wanted/batches", "application/json", body)
	if err != nil {
		t.Fatalf("POST /v1/wanted/batches: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		t.Fatalf("ingest answered %d; it outlived a lock it was never given a ceiling for", resp.StatusCode)
	}
}
