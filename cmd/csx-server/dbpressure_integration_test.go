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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/httpapi"
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

// openTestPG creates an isolated schema on CSX_TEST_DSN, runs migrations,
// registers cleanups, and returns the store plus an outside connection.
func openTestPG(t *testing.T, pol serverstore.PoolPolicy) (*serverstore.PG, *pgx.Conn) {
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
	return pg, outside
}

// openTestServer brings up the whole csx-server handler on a throwaway
// schema of CSX_TEST_DSN, and returns it plus a second connection outside
// the pool for the lock the fixture needs.
func openTestServer(t *testing.T, pol serverstore.PoolPolicy) (*httptest.Server, *pgx.Conn, *serverstore.PG) {
	t.Helper()
	pg, outside := openTestPG(t, pol)
	ctx := context.Background()
	cfg := serverstore.ServerConfig{
		PublicCheck: "trust",
		PublicURL:   "http://example.invalid",
		DBPool:      pol,
	}
	srv := httptest.NewServer(buildMux(ctx, cfg, pg))
	t.Cleanup(srv.Close)
	return srv, outside, pg
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
	srv, outside, _ := openTestServer(t, pol)
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
	srv, outside, _ := openTestServer(t, pol)
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
	srv, outside, _ := openTestServer(t, pol)

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

// TestIntegrationColdRestartWantedIsReadyDuringFirstBuilderPass reproduces
// the activation boundary from #174. The wanted snapshot is loaded before
// StartBuilder, the real first builder pass is then held on its first corpus
// read, and concurrent public requests must remain memory-only.
func TestIntegrationColdRestartWantedIsReadyDuringFirstBuilderPass(t *testing.T) {
	pol := testServerPoolPolicy()
	bootstrap, outside, pg := openTestServer(t, pol)
	bootstrap.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pg.RecordWanted(ctx, "2026-09-07", "coldstart0000001", []serverstore.WantedRow{{
		Ecosystem: "npm", Name: "cold-start-test", Version: "1.0.0",
	}}); err != nil {
		t.Fatalf("seed wanted: %v", err)
	}
	// These are the actual public release coordinates required by #174,
	// backed by materialized rows rather than a static or 404-only route.
	for _, pkg := range []serverstore.PackageRow{
		{PURL: "pkg:golang/github.com/jackc/pgx/v5@v5.10.0", Ecosystem: "golang", Name: "github.com/jackc/pgx/v5", Version: "v5.10.0"},
		{PURL: "pkg:golang/go.opentelemetry.io/otel@v1.45.0", Ecosystem: "golang", Name: "go.opentelemetry.io/otel", Version: "v1.45.0"},
	} {
		pkg.Publicness, pkg.FirstSeen, pkg.LastSeen = "PUBLIC", time.Now(), time.Now()
		if err := pg.UpsertPackage(ctx, pkg); err != nil {
			t.Fatal(err)
		}
		if err := pg.PutSnapshot(ctx, pkg.PURL, "", fmt.Sprintf(`{"purl":%q,"rows":[]}`, pkg.PURL)); err != nil {
			t.Fatal(err)
		}
		symbol := "Connect"
		if pkg.Name == "go.opentelemetry.io/otel" {
			symbol = "Tracer"
		}
		if err := pg.PutSnapshot(ctx, pkg.PURL, symbol, fmt.Sprintf(`{"purl":%q,"rows":[]}`, pkg.PURL)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := outside.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin builder lock: %v", err)
	}
	if _, err := outside.Exec(ctx, "LOCK TABLE evidence_agg IN ACCESS EXCLUSIVE MODE"); err != nil {
		_, _ = outside.Exec(context.Background(), "ROLLBACK")
		t.Fatalf("lock builder corpus: %v", err)
	}
	release := func() { _, _ = outside.Exec(context.Background(), "ROLLBACK") }
	defer release()

	cfg := serverstore.ServerConfig{
		PublicCheck:      "trust",
		PublicURL:        "http://example.invalid",
		DBPool:           pol,
		SnapshotInterval: time.Minute,
	}
	snapshot, err := primeWantedBeforeBuilder(ctx, cfg, pg, StartBuilder)
	if err != nil {
		t.Fatalf("prime wanted before builder: %v", err)
	}
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].Name != "cold-start-test" {
		t.Fatalf("primed wanted snapshot = %+v", snapshot.Rows)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if stat := classPoolStat(pg.PoolStats(), "background"); stat.InUse > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stat := classPoolStat(pg.PoolStats(), "background"); stat.InUse == 0 {
		t.Fatal("first builder pass never became active on the locked corpus read")
	}
	before := classPoolStat(pg.PoolStats(), "interactive")

	srv := httptest.NewServer(buildMuxWithWanted(ctx, cfg, pg, snapshot))
	defer srv.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	const readers = 12
	results := make(chan result, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			resp, err := client.Get(srv.URL + "/v1/wanted")
			if err != nil {
				results <- result{elapsed: time.Since(start), err: err}
				return
			}
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil && !strings.Contains(string(body), `"name":"cold-start-test"`) {
				readErr = fmt.Errorf("wanted response omitted the primed row: %s", body)
			}
			results <- result{status: resp.StatusCode, elapsed: time.Since(start), err: readErr}
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		if r.err != nil || r.status != http.StatusOK {
			t.Errorf("wanted during first builder pass status=%d elapsed=%v err=%v", r.status, r.elapsed, r.err)
		}
		if r.elapsed > 500*time.Millisecond {
			t.Errorf("wanted during first builder pass took %v, want <=500ms", r.elapsed)
		}
	}
	after := classPoolStat(pg.PoolStats(), "interactive")
	if after.Acquired != before.Acquired || after.Busy != before.Busy || after.Timeouts != before.Timeouts {
		t.Fatalf("wanted snapshot touched the interactive pool during builder work: before=%+v after=%+v", before, after)
	}
	for round := 0; round < 3; round++ {
		for _, path := range []string{"/", "/golang/github.com/jackc/pgx/v5/v5.10.0", "/golang/go.opentelemetry.io/otel/v1.45.0", "/v1/wanted"} {
			started := time.Now()
			resp, err := client.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("round %d %s: %v", round, path, err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("round %d %s status=%d err=%v", round, path, resp.StatusCode, err)
			}
			if path != "/v1/wanted" && !strings.Contains(string(body), `rel="canonical"`) {
				t.Fatalf("%s did not render a real page", path)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("round %d %s took %s", round, path, elapsed)
			}
			if stat := classPoolStat(pg.PoolStats(), "background"); stat.InUse == 0 {
				t.Fatal("builder no longer active during acceptance")
			}
		}
	}
	final := classPoolStat(pg.PoolStats(), "interactive")
	if final.Busy != before.Busy || final.Timeouts != before.Timeouts {
		t.Fatalf("public routes starved: before=%+v after=%+v", before, final)
	}
}

func buildMuxWithWanted(ctx context.Context, cfg serverstore.ServerConfig, store serverstore.Store, snapshot *httpapi.WantedSnapshot) http.Handler {
	mux, _ := buildMuxWithTrackerAndWanted(ctx, cfg, store, snapshot)
	return mux
}

func classPoolStat(stats serverstore.PoolStats, class string) serverstore.ClassPoolStats {
	for _, stat := range stats.Classes {
		if stat.Class == class {
			return stat
		}
	}
	return serverstore.ClassPoolStats{}
}
