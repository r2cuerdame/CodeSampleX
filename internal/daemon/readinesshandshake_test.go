package daemon

import (
	"slices"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// A recorded LAST handshake proves a first one happened, so the panel must not
// say the handshake never occurred two lines above printing its timestamp.
//
// From a farm report, one home's own `csx stats` output:
//
//	MCP handshake        —  never  → restart your coding agent, then use a csx tool
//	MCP last handshake   2026-08-30T14:23:27Z  (csx.db)
//
// The advice is wrong and the reader can see it is wrong, which is worse than
// saying nothing: the home had been running for days with config.json,
// identity.json and a csx.db full of hits, and was told to restart its agent
// because a stamp that predates the ledger is missing.
//
// The ledger already distinguishes "reached before this was recorded" from
// "never reached" — that distinction is what stopped the panel telling a
// days-old install to run csx init. This is the same case arriving through a
// different door: nothing wrote mcpFirstReadyAt, but mcpLastReadyAt is
// standing evidence that the stage was reached.
func TestALastHandshakeMeansTheFirstOneWasReached(t *testing.T) {
	led := localdb.Activation{
		MCPLastReadyAt: time.Date(2026, 8, 30, 14, 23, 27, 0, time.UTC),
		Unmeasured:     map[string]bool{},
	}
	r := readinessFrom(led)

	if r.MCPFirstReadyAt != "" {
		t.Fatalf("mcpFirstReadyAt = %q; this test is about it being absent", r.MCPFirstReadyAt)
	}
	if !slices.Contains(r.Unmeasured, "mcpFirstReadyAt") {
		t.Errorf("unmeasured = %v, want it to contain mcpFirstReadyAt: the panel will "+
			"otherwise say the handshake never happened and print its time on the next line",
			r.Unmeasured)
	}
}

// With neither stamp there is nothing to infer, and "never" is the honest
// answer along with the nudge that fixes it.
func TestNoHandshakeAtAllIsStillNever(t *testing.T) {
	r := readinessFrom(localdb.Activation{Unmeasured: map[string]bool{}})
	if slices.Contains(r.Unmeasured, "mcpFirstReadyAt") {
		t.Error("an install that never handshook was told the stage was reached")
	}
}

// And a recorded first handshake is left exactly as it is.
func TestARecordedFirstHandshakeIsNotRelabelled(t *testing.T) {
	at := time.Date(2026, 8, 30, 14, 0, 0, 0, time.UTC)
	r := readinessFrom(localdb.Activation{
		MCPFirstReadyAt: at,
		MCPLastReadyAt:  at.Add(time.Hour),
		Unmeasured:      map[string]bool{},
	})
	if r.MCPFirstReadyAt == "" {
		t.Fatal("a recorded first handshake was dropped")
	}
	if slices.Contains(r.Unmeasured, "mcpFirstReadyAt") {
		t.Error("a measured stamp was marked unmeasured")
	}
}
