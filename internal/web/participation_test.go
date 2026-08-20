package web

import (
	"strings"
	"testing"
)

// Participation counts remain part of the materialized stats document, but
// the compact homepage strip is intentionally not a general stats dashboard.
func TestParticipationNumbersNeverExpandHomepageStats(t *testing.T) {
	for _, peers := range []int64{0, 1, 5, 5_000} {
		tiles := buildTiles("en", &netStats{Peers: peers, ProjectsMonth: 73_000, Packages: 10}, 574)
		if len(tiles) != 3 {
			t.Errorf("peers=%d produced %d tiles, want 3", peers, len(tiles))
		}
		for _, tile := range tiles {
			if strings.Contains(tile.Label, "Projects this month") || strings.Contains(tile.Label, "Peers today") {
				t.Errorf("peers=%d rendered participation tile %q", peers, tile.Label)
			}
		}
	}
}
