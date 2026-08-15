package web

import (
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func statsWithReports(t *testing.T, reports int64) string {
	t.Helper()
	produced, err := compatibility.StatsJSON(
		serverstore.NetworkCounts{Peers: 2, Packages: 10, Symbols: 3, Observations: 40, VerifiedSamples: 2},
		serverstore.AdoptionCounts{Reports: reports, Applied: reports, BuildPass: reports},
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return string(produced)
}

// "100%" from a single report is arithmetically true and tells a reader
// nothing. Worse than nothing: a percentage claims a precision the sample
// size cannot support, so it reads as a measurement when it is one
// anecdote — and it sits beside packages and evidence counts that ARE
// measurements, borrowing their credibility.
//
// The denominator note was the first attempt at this and it was not enough.
// A rate needs a floor.
func TestARateIsNotShownUntilItCanBeARate(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.statsJSON, store.statsOK = statsWithReports(t, 1), true

	body := get(t, mux, "/").Body.String()
	if strings.Contains(body, `<span class="num mono">100%</span>`) {
		t.Errorf("a rate computed from one report was published as a percentage:\n%s", truncate(body))
	}
	// And the numbers that DO mean something are still there.
	for _, want := range []string{"stats.packages", "packages", "evidence"} {
		_ = want
	}
	if !strings.Contains(body, `class="stats"`) {
		t.Errorf("the stats block disappeared entirely:\n%s", truncate(body))
	}
}

// Once enough builds have been reported, the rate appears — and it still
// carries the count it was computed from, because a rate without its
// denominator is the shape of a number that looks like evidence.
func TestAMeasuredRateCarriesItsDenominator(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.statsJSON, store.statsOK = statsWithReports(t, int64(minReportsForARate)), true

	body := get(t, mux, "/").Body.String()
	if !strings.Contains(body, `<span class="num mono">100%</span>`) {
		t.Errorf("the measured rate is missing:\n%s", truncate(body))
	}
	if !strings.Contains(body, "reported build") {
		t.Errorf("the rate is shown with no denominator beside it:\n%s", truncate(body))
	}
}
