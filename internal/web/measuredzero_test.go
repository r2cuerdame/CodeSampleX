package web

import (
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A measured adoption result is still serialized by the producer, but it is
// not one of the three headline counts selected for the homepage.
func TestMeasuredAdoptionResultStaysOutOfHomepageCards(t *testing.T) {
	produced, err := compatibility.StatsJSON(
		serverstore.NetworkCounts{Peers: 2, Packages: 10, Symbols: 3, Observations: 40, VerifiedSamples: 2},
		serverstore.AdoptionCounts{Reports: 24, Applied: 24, BuildPass: 0, BuildFail: 24},
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	mux, store := newTestMux(t, nil)
	store.statsJSON, store.statsOK = string(produced), true

	body := get(t, mux, "/").Body.String()
	if strings.Contains(body, `<span class="num mono">0%</span>`) || strings.Contains(body, "Post-hit success rate") {
		t.Errorf("adoption rate leaked into focused homepage stats:\n%s", truncate(body))
	}
	if got := strings.Count(body, `<div class="stat">`); got != 2 {
		t.Errorf("homepage stat cards = %d, want 2", got)
	}
}
