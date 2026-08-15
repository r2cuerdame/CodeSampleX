package web

import (
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// "100%" from a single report is arithmetically true and tells a reader
// nothing — it is the shape of a number that looks like evidence and is
// not. The rate now carries the count it was computed from, so a visitor
// deciding whether to install can see immediately how much it is worth.
func TestThePostHitRateCarriesItsDenominator(t *testing.T) {
	produced, err := compatibility.StatsJSON(
		serverstore.NetworkCounts{Peers: 2, Packages: 10, Symbols: 3, Observations: 40, VerifiedSamples: 2},
		serverstore.AdoptionCounts{Reports: 1, Applied: 1, BuildPass: 1},
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	mux, store := newTestMux(t, nil)
	store.statsJSON, store.statsOK = string(produced), true

	body := get(t, mux, "/").Body.String()
	if !strings.Contains(body, `<span class="num mono">100%</span>`) {
		t.Errorf("the measured rate is missing:\n%s", truncate(body))
	}
	if !strings.Contains(body, "of 1 reported build") {
		t.Errorf("100%% is shown with no denominator beside it:\n%s", truncate(body))
	}
}

// With nothing reported there is no denominator to show and no rate either.
func TestNoDenominatorWhenNothingWasReported(t *testing.T) {
	produced, err := compatibility.StatsJSON(
		serverstore.NetworkCounts{Peers: 2, Packages: 10, Symbols: 3, Observations: 40, VerifiedSamples: 2},
		serverstore.AdoptionCounts{},
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	mux, store := newTestMux(t, nil)
	store.statsJSON, store.statsOK = string(produced), true
	body := get(t, mux, "/").Body.String()
	if strings.Contains(body, "reported build") {
		t.Errorf("a denominator was shown with nothing behind it:\n%s", truncate(body))
	}
}
