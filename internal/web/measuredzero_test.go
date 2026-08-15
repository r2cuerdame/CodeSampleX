package web

import (
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// "Not measured" was decided from the RATE, so a genuine measured 0% —
// adoption reports arrived and every reported build failed — rendered as an
// em dash, which on this page means "nobody has told us". The one number
// worth showing was the one it refused to show, and it refused in the
// direction that flatters.
func TestAMeasuredZeroIsShownAsZero(t *testing.T) {
	produced, err := compatibility.StatsJSON(
		serverstore.NetworkCounts{Peers: 2, Packages: 10, Symbols: 3, Observations: 40, VerifiedSamples: 2},
		serverstore.AdoptionCounts{Reports: 12, Applied: 12, BuildPass: 0, BuildFail: 12},
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	mux, store := newTestMux(t, nil)
	store.statsJSON, store.statsOK = string(produced), true

	body := get(t, mux, "/").Body.String()
	if !strings.Contains(body, `<span class="num mono">0%</span>`) {
		t.Errorf("a measured 0%% was not shown:\n%s", truncate(body))
	}
}

// And an unmeasured rate still says nothing rather than 0%.
func TestAnUnmeasuredRateStaysAnEmDash(t *testing.T) {
	produced, err := compatibility.StatsJSON(
		serverstore.NetworkCounts{Peers: 2, Packages: 10, Symbols: 3, Observations: 40, VerifiedSamples: 2},
		serverstore.AdoptionCounts{Reports: 4, Applied: 4}, // nobody reported a build
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	mux, store := newTestMux(t, nil)
	store.statsJSON, store.statsOK = string(produced), true
	body := get(t, mux, "/").Body.String()
	if strings.Contains(body, `<span class="num mono">0%</span>`) {
		t.Errorf("an unmeasured rate was rendered as 0%%:\n%s", truncate(body))
	}
}
