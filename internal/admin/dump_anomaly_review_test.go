package admin

import (
	"os"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Dumps the dashboard with the anomaly channel populated, for a human to
// look at. A CSS read is a hypothesis; the rendered page is the answer.
// Off by default: set CSX_DUMP_ADMIN to a path.
func TestDumpAdminWithAnomalyChannel(t *testing.T) {
	out := os.Getenv("CSX_DUMP_ADMIN")
	if out == "" {
		t.Skip("set CSX_DUMP_ADMIN=<path> to write the rendered dashboard")
	}
	anomalies := &fakeAnomalyStore{
		insights: serverstore.AnomalyInsights{
			WindowStart: anomalyNow.AddDate(0, 0, -30), WindowEnd: anomalyNow,
			Reports: 41, Unique: 17, Duplicates: 24,
			Queued: 6, Verifying: 2, Verified: 8, Unsupported: 1,
			Confirmed: 3, CSXDefects: 2, Insufficient: 1,
			VerdictLatencyTotal: 9 * time.Hour, VerdictLatencyCount: 8,
			VerdictLatencyMax: 4 * time.Hour,
			BusiestReporter:   26,
			Verdicts: []serverstore.AnomalyVerdictCounts{
				{Verdict: domain.AnomalyVerdictNotReproducible, Count: 4},
				{Verdict: domain.AnomalyVerdictCSXDefect, Count: 2},
				{Verdict: domain.AnomalyVerdictEnvironmentDifference, Count: 1},
				{Verdict: domain.AnomalyVerdictNewEvidence, Count: 1},
			},
		},
		rows: []serverstore.AnomalyReportRow{
			{ID: 41, AnomalyType: domain.AnomalyCSXPassLocalFail, PURL: "pkg:npm/axios@1.12.0",
				Symbol: "axios.post", Status: domain.AnomalyStatusVerified,
				Verdict: domain.AnomalyVerdictCSXDefect, Reports: 9, JobID: 4471,
				FirstSeen: anomalyNow.Add(-3 * time.Hour)},
			{ID: 40, AnomalyType: domain.AnomalyCSXFailLocalPass, PURL: "pkg:pypi/httpx@0.27.0",
				Status: domain.AnomalyStatusVerifying, Reports: 2, JobID: 4470,
				FirstSeen: anomalyNow.Add(-40 * time.Minute)},
			{ID: 39, AnomalyType: domain.AnomalyRepeatedNoSafeMatch, PURL: "pkg:cargo/tokio@1.40.0",
				Status: domain.AnomalyStatusUnsupported, Reports: 1,
				UnsupportedReason: "no verifier lane: the report names no sample this network published",
				FirstSeen:         anomalyNow.Add(-26 * time.Hour)},
		},
	}
	mux, secret := configuredMuxWithAnomalies(t, &fakeStore{}, anomalies)
	body := anomalyPanelBody(t, mux, secret)
	if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d bytes to %s", len(body), out)
}
