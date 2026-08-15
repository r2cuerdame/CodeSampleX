package web

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// Peer buckets rotate daily and the operator's own machine is always one of
// them, so a count of 1 cannot be told apart from no external activity —
// and shown as a network statistic it implies activity that is not there.
// The adoption detector reads it the same way: its baseline is one peer
// today, meaning us.
func TestPeerTileHiddenWhileItCannotMeanAnything(t *testing.T) {
	labelFor := func(st netStats) []string {
		var out []string
		for _, tile := range buildTiles(i18n.Default, &st) {
			out = append(out, tile.Label)
		}
		return out
	}
	peers := i18n.T(i18n.Default, "stats.peers")

	// Two is what one person running a laptop and a test container
	// produces -- confirmed the hard way: a container I started for an
	// install test became the second "peer", and the tile read as
	// participation. A peer count only carries information well above the
	// number of machines one operator runs.
	for _, n := range []int64{0, 1, 2, 4} {
		for _, l := range labelFor(netStats{Peers: n}) {
			if l == peers {
				t.Errorf("peers=%d still rendered a peer tile", n)
			}
		}
	}
	var found bool
	for _, l := range labelFor(netStats{Peers: minPeersToShow}) {
		if l == peers {
			found = true
		}
	}
	if !found {
		t.Errorf("peers=%d should be shown", minPeersToShow)
	}
}

// The adoption-derived figures have no data behind them until a real user
// applies a sample and reports back. "0" reads as "we measured and nobody
// was helped"; the truth is that nothing has been collected yet, and those
// are different claims.
func TestUnmeasuredFiguresRenderAsADashNotZero(t *testing.T) {
	// Above the floor, where the tile is shown at all: with reports in
	// hand but nothing derived yet, a dash and a zero are different claims.
	var reasoning string
	for _, tile := range buildTiles(i18n.Default, &netStats{PostHitBuildsReported: minReportsForARate}) {
		if tile.Label == i18n.T(i18n.Default, "stats.reasoning_avoided") {
			reasoning = tile.Value
		}
	}
	if reasoning == "" {
		t.Fatal("the reasoning-avoided tile disappeared")
	}
	if strings.TrimSpace(reasoning) == "0" {
		t.Error("rendered 0 for a figure nothing has been collected for")
	}
}
