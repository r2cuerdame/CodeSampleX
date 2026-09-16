package main

// CSX_BUILDER_MODE=standalone (CSX-451) must stop this process from ever
// running a second copy of the aggregation pipeline once a dedicated
// cmd/csx-builder process exists. These tests pin the one call site that
// decides that -- primeWantedBeforeBuilder -- so a future refactor of the
// boot sequence cannot silently start both.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestPrimeWantedBeforeBuilderStartsTheInProcessBuilderByDefault(t *testing.T) {
	store := serverstore.NewFake()
	started := false
	cfg := serverstore.ServerConfig{PublicCheck: "trust"}
	if _, err := primeWantedBeforeBuilder(context.Background(), cfg, store, func(context.Context, serverstore.ServerConfig, serverstore.Store) {
		started = true
	}); err != nil {
		t.Fatalf("primeWantedBeforeBuilder: %v", err)
	}
	if !started {
		t.Fatal("the in-process builder was not started with an unset CSX_BUILDER_MODE, which must keep today's behaviour")
	}
}

func TestPrimeWantedBeforeBuilderSkipsTheInProcessBuilderInStandaloneMode(t *testing.T) {
	store := serverstore.NewFake()
	started := false
	cfg := serverstore.ServerConfig{PublicCheck: "trust", BuilderMode: serverstore.BuilderModeStandalone}
	if _, err := primeWantedBeforeBuilder(context.Background(), cfg, store, func(context.Context, serverstore.ServerConfig, serverstore.Store) {
		started = true
	}); err != nil {
		t.Fatalf("primeWantedBeforeBuilder: %v", err)
	}
	if started {
		t.Fatal("the in-process builder ran under CSX_BUILDER_MODE=standalone; a separate cmd/csx-builder process now owns it, and running both duplicates work")
	}
}

func TestConfigFromEnvDefaultsToInProcessBuilderMode(t *testing.T) {
	t.Setenv("CSX_BUILDER_MODE", "")
	if got := serverstore.ConfigFromEnv().BuilderMode; got != serverstore.BuilderModeInProcess {
		t.Fatalf("BuilderMode = %q, want %q", got, serverstore.BuilderModeInProcess)
	}
}

func TestConfigFromEnvReadsStandaloneBuilderMode(t *testing.T) {
	t.Setenv("CSX_BUILDER_MODE", "standalone")
	if got := serverstore.ConfigFromEnv().BuilderMode; got != serverstore.BuilderModeStandalone {
		t.Fatalf("BuilderMode = %q, want %q", got, serverstore.BuilderModeStandalone)
	}
}

// TestStartBuilderContendsForTheSameLeaseAStandaloneBuilderWould pins the
// cutover safety property docs/operations.md "Builder runtime topology"
// describes: `docker compose up` with no service names brings up every
// defined service, so a `builder` container can exist and be running
// alongside a `server` container still in CSX_BUILDER_MODE=inprocess. That
// must never mean two aggregation pipelines materializing the same shards
// at once -- StartBuilder must lose the race to whoever already holds
// compatibility-builder, exactly as a second standalone csx-builder would.
func TestStartBuilderContendsForTheSameLeaseAStandaloneBuilderWould(t *testing.T) {
	store := serverstore.NewFake()

	// A standalone Builder (a different process, a different owner) already
	// holds the lease when this csx-server instance boots.
	if _, err := store.AcquireBuilderLease(context.Background(), compatibility.DefaultLeaseName, "csx-builder:other-host:1:1", time.Hour); err != nil {
		t.Fatalf("seed standalone lease: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := serverstore.ServerConfig{SnapshotInterval: time.Millisecond}
	StartBuilder(ctx, cfg, store)

	// StartBuilder's own RunOnce would show up as background-class pool
	// activity; give it a moment to (wrongly) start if the lease were not
	// respected, then confirm it never became the lease holder.
	time.Sleep(100 * time.Millisecond)
	lease, ok, err := store.GetBuilderLease(context.Background(), compatibility.DefaultLeaseName)
	if err != nil || !ok {
		t.Fatalf("get lease: ok=%v err=%v", ok, err)
	}
	if lease.Owner != "csx-builder:other-host:1:1" {
		t.Fatalf("lease owner = %q; the in-process Builder took over a lease a standalone Builder already held", lease.Owner)
	}
}

// The reverse: with nobody else holding the lease, StartBuilder must still
// acquire it and actually run -- the lease must never become a way to
// silently disable the in-process Builder.
func TestStartBuilderRunsWhenNobodyElseHoldsTheLease(t *testing.T) {
	store := serverstore.NewFake()
	var passes atomic.Int64
	store.NowFn = time.Now

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := serverstore.ServerConfig{SnapshotInterval: time.Millisecond}
	StartBuilder(ctx, cfg, store)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if lease, ok, err := store.GetBuilderLease(context.Background(), compatibility.DefaultLeaseName); err == nil && ok && lease.Owner != "" {
			passes.Store(1)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if passes.Load() == 0 {
		t.Fatal("StartBuilder never acquired the builder lease with nobody else contending for it")
	}
}
