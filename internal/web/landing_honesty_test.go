package web

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// The homepage answers the three questions a visitor can act on: how many
// packages have coverage, how much evidence backs it, and how many runnable
// samples were verified. The producer still carries the other raw fields for
// API and detail consumers; even large values there must not grow this strip.
func TestHomepageUsesExactlyFourCountersInEveryLocale(t *testing.T) {
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
			tiles := buildTiles(lang, stats, 574)
			// One order, in every language: what was measured, how far it
			// reaches, what we produced, what we learned. The row ends on
			// what the project stands behind — a finding is a measured
			// correction with the sample that proves it — and it opens on
			// the raw record everything else rests on.
			if len(tiles) != 4 {
				t.Fatalf("tiles = %d, want 4: %#v", len(tiles), tiles)
			}
			wantLabels := []string{
				i18n.T(lang, "stats.observations"),
				i18n.T(lang, "stats.packages"),
				i18n.T(lang, "stats.verified_samples"),
				i18n.T(lang, "stats.findings"),
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

// A stats outage blanks the counters that come from the stats document, and
// only those. The findings count is read straight from the findings the site
// already holds, so an em dash there would claim the page does not know a
// number it does know.
func TestUnavailableHomepageStatsRemainHonestPlaceholders(t *testing.T) {
	tiles := buildTiles(i18n.Default, nil, 574)
	if len(tiles) != 4 {
		t.Fatalf("tiles = %d, want 4: %#v", len(tiles), tiles)
	}
	for _, tile := range tiles[:3] {
		if tile.Value != "—" {
			t.Errorf("%s value = %q, want placeholder", tile.Label, tile.Value)
		}
		if tile.Exact != "" {
			t.Errorf("%s unavailable value exposed exact text %q", tile.Label, tile.Exact)
		}
	}
	if got := tiles[3]; got.Value != "574" || got.Exact != "574" {
		t.Errorf("findings counter = %q/%q, want the count it knows", got.Value, got.Exact)
	}
}
