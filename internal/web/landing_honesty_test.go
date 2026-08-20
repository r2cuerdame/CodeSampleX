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
			// One order, in every language: what was measured, what we made
			// of it, how far it reaches. Our own output stays in the middle.
			// The findings count was a fourth card here and moved to its own
			// page — four cards asked the reader to weigh four different
			// units at once.
			if len(tiles) != 3 {
				t.Fatalf("tiles = %d, want 3: %#v", len(tiles), tiles)
			}
			wantLabels := []string{
				i18n.T(lang, "stats.observations"),
				i18n.T(lang, "stats.verified_samples"),
				i18n.T(lang, "stats.packages"),
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

// A stats outage blanks every counter, because every counter now comes from
// the stats document. The findings count used to sit here reading from a
// different source and staying real through an outage; it moved to its own
// page when the row went back to three.
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
