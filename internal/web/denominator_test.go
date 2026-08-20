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

// Adoption metrics remain present in the raw producer document. Whether the
// denominator is tiny or substantial, they do not turn the focused homepage
// strip into a full stats dashboard.
func TestAdoptionMetricsNeverExpandHomepageStats(t *testing.T) {
	for _, reports := range []int64{1, 20, 20_000} {
		t.Run(time.Unix(reports, 0).UTC().Format("150405"), func(t *testing.T) {
			mux, store := newTestMux(t, nil)
			store.statsJSON, store.statsOK = statsWithReports(t, reports), true

			body := get(t, mux, "/").Body.String()
			if got := strings.Count(body, `<div class="stat">`); got != 2 {
				t.Errorf("reports=%d rendered %d stat cards, want 2", reports, got)
			}
			// "100%" left this list when cells started stating a percentage:
			// a matrix cell reading 100% is a measurement, not an adoption
			// metric, and the named phrases are what this guard is actually
			// about.
			for _, omitted := range []string{"Post-hit success rate", "Estimated reasoning avoided", "reported build"} {
				if strings.Contains(body, omitted) {
					t.Errorf("reports=%d rendered adoption metric %q", reports, omitted)
				}
			}
		})
	}
}
