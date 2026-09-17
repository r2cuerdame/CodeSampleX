package main

// CSX-451's central claim, proven against real PostgreSQL rather than
// argued from the code: the standalone Builder's connection pool and
// csx-server's connection pool are two independent *pgx.Conn pools (two Go
// structs, two semaphores, two sets of gates), not one shared pool with an
// extra QueryClass. Saturating one must be invisible to the other.
//
// dbpressure_integration_test.go already proved the in-process, single-pool
// world: ClassBackground and ClassInteractive share one connPool and stay
// apart because of per-class gates inside it. This file proves the world
// CSX-451 replaces it with: even when the Builder's *entire* pool is held
// (every connection it is allowed to open at all, not merely its share of
// somebody else's), csx-server's pool is untouched, because there is
// nothing shared between them left to touch.

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

type separatedPools struct {
	srv                   *httptest.Server
	outside               *pgx.Conn
	serverPG, builderPG   *serverstore.PG
	serverPol, builderPol serverstore.PoolPolicy
	scopedDSN             string
}

// openSeparatedPools brings up csx-server's HTTP handler on its own small
// pool and a second, independent PG store sized like the standalone
// Builder's, both against the same throwaway schema of CSX_TEST_DSN -- the
// same database two real processes on csx-prod-1 would share.
func openSeparatedPools(t *testing.T) separatedPools {
	t.Helper()
	dsn := os.Getenv("CSX_TEST_DSN")
	if dsn == "" {
		if require := os.Getenv("CSX_REQUIRE_TEST_DSN"); require != "" {
			if off, err := strconv.ParseBool(require); err != nil || !off {
				t.Fatalf("CSX_TEST_DSN is empty while CSX_REQUIRE_TEST_DSN=%s demands the PostgreSQL suite", require)
			}
		}
		t.Skip("CSX_TEST_DSN is not set; skipping the PostgreSQL builder-separation test")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("csx_sep_%d", time.Now().UnixNano())

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close(ctx)
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})
	if _, err := admin.Exec(ctx, "SET search_path = "+schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	scopedDSN := dsn + sep + "search_path=" + schema

	serverPol := testServerPoolPolicy()
	serverPG, err := serverstore.OpenWithPolicy(ctx, scopedDSN, serverPol)
	if err != nil {
		t.Fatalf("open server store: %v", err)
	}
	t.Cleanup(serverPG.Close)
	if err := serverPG.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	builderPol := serverstore.DefaultBuilderPoolPolicy()
	builderPG, err := serverstore.OpenWithPolicy(ctx, scopedDSN, builderPol)
	if err != nil {
		t.Fatalf("open builder store: %v", err)
	}
	t.Cleanup(builderPG.Close)

	cfg := serverstore.ServerConfig{PublicCheck: "trust", PublicURL: "http://example.invalid", DBPool: serverPol}
	srv := httptest.NewServer(buildMux(ctx, cfg, serverPG))
	t.Cleanup(srv.Close)

	return separatedPools{srv, admin, serverPG, builderPG, serverPol, builderPol, scopedDSN}
}

// lockBuilderLeaseTable makes any query that touches builder_lease block
// until release is called -- a real PostgreSQL lock, the same fixture shape
// dbpressure_integration_test.go uses for /wanted.
func lockBuilderLeaseTable(t *testing.T, conn *pgx.Conn) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := conn.Exec(ctx, "LOCK TABLE builder_lease IN ACCESS EXCLUSIVE MODE"); err != nil {
		_, _ = conn.Exec(ctx, "ROLLBACK")
		t.Fatalf("lock builder_lease: %v", err)
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

func classStatOf(stats serverstore.PoolStats, class string) serverstore.ClassPoolStats {
	for _, s := range stats.Classes {
		if s.Class == class {
			return s
		}
	}
	return serverstore.ClassPoolStats{}
}

// TestIntegrationBuilderPoolSaturationDoesNotStarveWebAPI is the
// acceptance test CSX-451 asks for directly: drive the standalone Builder's
// pool to its configured maximum while csx-server's public routes stay
// inside SLO, and prove the isolation is physical (a second pool) rather
// than accounting (a second class sharing one pool).
func TestIntegrationBuilderPoolSaturationDoesNotStarveWebAPI(t *testing.T) {
	pools := openSeparatedPools(t)
	release := lockBuilderLeaseTable(t, pools.outside)

	// Every connection the Builder's pool may ever open, all doing real
	// work that is blocked on the lock above -- its whole capacity, not
	// merely its class share of a bigger shared pool.
	inFlight := pools.builderPol.MaxConns + 2
	var wg sync.WaitGroup
	for i := 0; i < inFlight; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := serverstore.WithQueryClass(context.Background(), serverstore.ClassBackground)
			_, _, _ = pools.builderPG.GetBuilderLease(ctx, "compatibility-builder")
		}()
	}

	// Give the Builder's pool time to actually saturate before measuring.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if classStatOf(pools.builderPG.PoolStats(), "background").InUse >= pools.builderPol.BackgroundConns {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if inUse := classStatOf(pools.builderPG.PoolStats(), "background").InUse; inUse < pools.builderPol.BackgroundConns {
		t.Fatalf("builder pool never saturated: background InUse=%d, want >= %d", inUse, pools.builderPol.BackgroundConns)
	}

	before := classStatOf(pools.serverPG.PoolStats(), "interactive")
	client := &http.Client{Timeout: 5 * time.Second}
	const rounds = 5
	for round := 0; round < rounds; round++ {
		for _, path := range []string{"/healthz", "/features", "/v1/wanted"} {
			started := time.Now()
			resp, err := client.Get(pools.srv.URL + path)
			if err != nil {
				t.Fatalf("round %d %s: %v", round, path, err)
			}
			resp.Body.Close()
			if resp.StatusCode >= 500 {
				t.Errorf("round %d %s answered %d while the builder pool was saturated", round, path, resp.StatusCode)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Errorf("round %d %s took %v while the builder pool was saturated, want <=1s (SLO)", round, path, elapsed.Round(time.Millisecond))
			}
		}
	}
	after := classStatOf(pools.serverPG.PoolStats(), "interactive")
	if after.Busy != before.Busy || after.Timeouts != before.Timeouts {
		t.Fatalf("csx-server's own pool absorbed pressure from the builder pool: before=%+v after=%+v", before, after)
	}

	release()
	wg.Wait()
}

// TestIntegrationBuilderProcessCrashDoesNotAffectWebAPI simulates the OS
// process kill CSX-451 asks for: the standalone Builder's connection is
// torn down mid-work without any cooperation from csx-server, and csx-server
// must keep answering throughout and afterward -- because it never held a
// reference to the Builder's pool to lose.
func TestIntegrationBuilderProcessCrashDoesNotAffectWebAPI(t *testing.T) {
	pools := openSeparatedPools(t)
	release := lockBuilderLeaseTable(t, pools.outside)

	inFlight := pools.builderPol.MaxConns
	for i := 0; i < inFlight; i++ {
		go func() {
			ctx := serverstore.WithQueryClass(context.Background(), serverstore.ClassBackground)
			_, _, _ = pools.builderPG.GetBuilderLease(ctx, "compatibility-builder")
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if classStatOf(pools.builderPG.PoolStats(), "background").InUse > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The kill: close the Builder's pool out from under its own in-flight
	// work, the way an OS process kill would. csx-server's pool is a
	// different object entirely and is never told this happened.
	pools.builderPG.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	for round := 0; round < 5; round++ {
		resp, err := client.Get(pools.srv.URL + "/healthz")
		if err != nil {
			t.Fatalf("round %d /healthz: %v", round, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("round %d /healthz answered %d after the builder process was killed", round, resp.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
	release()

	// A restart: a fresh Builder process (a fresh pool, a fresh connection)
	// against the same database must work normally, proving the crash left
	// nothing the next instance has to recover from beyond its lease (see
	// internal/serverstore/lease_pg_test.go), and csx-server must stay
	// healthy throughout that restart too.
	restarted, err := serverstore.OpenWithPolicy(context.Background(), pools.scopedDSN, pools.builderPol)
	if err != nil {
		t.Fatalf("reopen builder pool after crash (restart): %v", err)
	}
	defer restarted.Close()
	if _, _, err := restarted.GetBuilderLease(serverstore.WithQueryClass(context.Background(), serverstore.ClassBackground), "compatibility-builder"); err != nil {
		t.Fatalf("restarted builder pool cannot read the lease table: %v", err)
	}
	if resp, err := client.Get(pools.srv.URL + "/healthz"); err != nil || resp.StatusCode != http.StatusOK {
		status := 0
		if resp != nil {
			status = resp.StatusCode
			resp.Body.Close()
		}
		t.Fatalf("/healthz after builder restart: status=%d err=%v", status, err)
	} else {
		resp.Body.Close()
	}
}
