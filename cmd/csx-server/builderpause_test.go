package main

// #454, the topology half: CSX_BUILDER_MODE defaults to inprocess and stays
// there until #455's rollout completes, so a governor pause that only reached
// cmd/csx-builder would do nothing on the deployment production actually
// runs. The gate is one assignment in newInProcessBuilder, and this is what
// notices when it is gone -- the governor's own tests pause a fake, and the
// acceptance test asserts the lease flag rather than this Builder's use of
// it.

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestInProcessBuilderObeysTheGovernorsPause(t *testing.T) {
	// The gate logs its transitions through the standard logger; this test is
	// about the wiring, not the lines.
	defer captureStandardLog(io.Discard)()

	store := serverstore.NewFake()
	b, leader := newInProcessBuilder(serverstore.ServerConfig{}, store)
	ctx := context.Background()

	if b.Paused == nil {
		t.Fatal("the in-process Builder has no pause gate; on CSX_BUILDER_MODE=inprocess the governor would pause into the void")
	}
	if b.Paused(ctx) {
		t.Fatal("the Builder reports paused with nothing paused")
	}

	// The governor writes the pause under the default lease name, from a
	// different owner identity than this Builder holds. Both halves matter:
	// the name is what makes the two processes talk about the same work, and
	// the owner is what a fenced write would have rejected.
	if err := store.PauseBuilderLease(ctx, compatibility.DefaultLeaseName, time.Minute); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if !b.Paused(ctx) {
		t.Fatalf("the in-process Builder did not see a pause written under %q", compatibility.DefaultLeaseName)
	}
	if !leader.Status().Paused {
		t.Fatal("the Leader's reported status does not carry the pause it just read")
	}

	if err := store.ResumeBuilderLease(ctx, compatibility.DefaultLeaseName); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if b.Paused(ctx) {
		t.Fatal("the Builder stayed paused after the governor cleared it")
	}
}
