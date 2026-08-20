package web

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// The observation record is what the network actually measured: builds that
// really ran, in environments recorded rather than assumed. It came off the
// front page this morning because the figure was wrong three ways — the same
// build counted once per symbol found in it, presence records that cannot
// fail folded into it, and events named as though they were people. All three
// are fixed, and the corrected number is the plainest true thing this project
// can say about itself.
func TestHomepageReportsTheObservationRecord(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.statsJSON = `{"packages":1888,"verifiedSamples":2736,"symbols":497,"evidence":34122}`
	f.statsOK = true
	body := get(t, mux, "/").Body.String()

	mustContain(t, body, `aria-label="Observations: 34,122"`)
	if got := strings.Count(body, `<div class="stat">`); got != 4 {
		t.Errorf("stat cards = %d, want 4", got)
	}
}

// Four counters, in every language, in one order: what was measured, how far
// it reaches, what we produced, what we learned.
func TestHomepageCountersReadInOneOrderInEveryLocale(t *testing.T) {
	stats := &netStats{Packages: 1888, Evidence: 34122, VerifiedSamples: 2736}
	for _, lang := range i18n.Supported {
		tiles := buildTiles(lang, stats, 572)
		if len(tiles) != 4 {
			t.Fatalf("%s: tiles = %d, want 4", lang, len(tiles))
		}
		want := []string{
			i18n.T(lang, "stats.observations"),
			i18n.T(lang, "stats.packages"),
			i18n.T(lang, "stats.verified_samples"),
			i18n.T(lang, "stats.findings"),
		}
		for i, label := range want {
			if tiles[i].Label != label {
				t.Errorf("%s: tile %d = %q, want %q", lang, i, tiles[i].Label, label)
			}
		}
	}
}
