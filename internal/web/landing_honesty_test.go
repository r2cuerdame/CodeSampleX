package web

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// The homepage answers the three questions a visitor can act on: how many
// packages have coverage, how much evidence backs it, and how many runnable
// samples were verified. The producer still carries the other raw fields for
// API and detail consumers; even large values there must not grow this strip.
func TestHomepageUsesExactlyThreeCountersInEveryLocale(t *testing.T) {
	stats := &netStats{
		Peers:                     99,
		ProjectsMonth:             88,
		Packages:                  17_500,
		Symbols:                   77_777,
		Evidence:                  45_213,
		VerifiedSamples:           1_234,
		PostHitSuccessRate:        1,
		PostHitBuildsReported:     1_000,
		EstimatedReasoningAvoided: estimatedNumber{Value: 3_000, Estimated: true},
	}
	for _, lang := range i18n.Supported {
		t.Run(lang, func(t *testing.T) {
			tiles := buildTiles(lang, stats)
			// Coverage on the outside, our own output in the middle. The
			// verified-sample count is the one number here that measures US
			// rather than the ecosystem, and it is the largest: ending the row
			// on it let it read as the conclusion of a coverage story.
			if len(tiles) != 3 {
				t.Fatalf("tiles = %d, want 3: %#v", len(tiles), tiles)
			}
			wantLabels := []string{
				i18n.T(lang, "stats.packages"),
				i18n.T(lang, "stats.verified_samples"),
				i18n.T(lang, "stats.apis"),
			}
			for i, want := range wantLabels {
				if tiles[i].Label != want {
					t.Errorf("tile %d label = %q, want %q", i, tiles[i].Label, want)
				}
				if tiles[i].Exact == "" {
					t.Errorf("tile %d has no exact accessible value", i)
				}
			}
		})
	}
}

func TestUnavailableHomepageStatsRemainHonestPlaceholders(t *testing.T) {
	tiles := buildTiles(i18n.Default, nil)
	if len(tiles) != 3 {
				t.Fatalf("tiles = %d, want 3: %#v", len(tiles), tiles)
			}
	for _, tile := range tiles {
		if tile.Value != "—" {
			t.Errorf("%s value = %q, want placeholder", tile.Label, tile.Value)
		}
		if tile.Exact != "" {
			t.Errorf("%s unavailable value exposed exact text %q", tile.Label, tile.Exact)
		}
	}
}
