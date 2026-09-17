package main

// #454: this process must actually obey the resource governor's pause.
//
// The wiring is one assignment (newBuilder's builder.Paused = ...), and
// without a test here every other test in the repository stays green when it
// is deleted: internal/compatibility tests the gate and the loop directly
// with hand-built Builders, and csx-server's acceptance test asserts the
// lease flag rather than this binary's use of it. So the thing pinned here is
// the wiring itself -- that the Builder this command runs answers the flag a
// governor writes, through the lease name this command was configured with.

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestStandaloneBuilderObeysTheGovernorsPause(t *testing.T) {
	// The gate logs its transitions through the standard logger; this test is
	// about the wiring, not the lines.
	prev := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prev)

	store := serverstore.NewFake()
	cfg := serverstore.BuilderConfig{
		LeaseName:  "compatibility-builder",
		LeaseOwner: "csx-builder-test",
		LeaseTTL:   time.Minute,
	}
	builder, leader := newBuilder(store, cfg, &passTracker{})
	ctx := context.Background()

	if builder.Paused == nil {
		t.Fatal("the standalone Builder has no pause gate; the governor would pause into the void")
	}
	if builder.Paused(ctx) {
		t.Fatal("the Builder reports paused with nothing paused")
	}

	// The governor is a different process writing the same lease row, so the
	// pause is written through the store rather than through this Leader.
	if err := store.PauseBuilderLease(ctx, cfg.LeaseName, time.Minute); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if !builder.Paused(ctx) {
		t.Fatal("the Builder did not see a pause written on its own lease name")
	}
	if !leader.Status().Paused {
		t.Fatal("/progress would not report the pause the Builder is obeying")
	}

	if err := store.ResumeBuilderLease(ctx, cfg.LeaseName); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if builder.Paused(ctx) {
		t.Fatal("the Builder stayed paused after the governor cleared it")
	}
}
