package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

var anomalyNow = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

// A mean over nothing is absent. 0ms on a channel that has answered nothing
// is a number an operator would act on, and acting on it would be wrong.
func TestAnomalyLatencyIsAbsentRatherThanZero(t *testing.T) {
	view := buildAnomalyView(serverstore.AnomalyInsights{
		WindowStart: anomalyNow.AddDate(0, 0, -30), WindowEnd: anomalyNow,
		Reports: 4, Unique: 4, Queued: 4,
	}, nil, anomalyNow)
	if view.MeanLatency != "—" || view.MaxLatency != "—" {
		t.Fatalf("unmeasured latency rendered as %q / %q", view.MeanLatency, view.MaxLatency)
	}
	if view.ConfirmedRate != "—" {
		t.Fatalf("a confirmed rate with no verdicts behind it reads %q", view.ConfirmedRate)
	}
}

// The two numbers that decide what an operator does: a channel finding real
// defects wants attention on the defects, a channel full of one client's
// retries wants attention on the client.
func TestAnomalyViewSeparatesConfirmationsFromRepetition(t *testing.T) {
	view := buildAnomalyView(serverstore.AnomalyInsights{
		WindowStart: anomalyNow.AddDate(0, 0, -30), WindowEnd: anomalyNow,
		Reports: 10, Unique: 4, Duplicates: 6,
		Queued: 1, Verifying: 1, Verified: 2,
		Confirmed: 1, CSXDefects: 1, Insufficient: 1,
		VerdictLatencyTotal: 3 * time.Hour, VerdictLatencyCount: 2,
		VerdictLatencyMax: 2 * time.Hour,
		BusiestReporter:   8,
		Verdicts: []serverstore.AnomalyVerdictCounts{
			{Verdict: domain.AnomalyVerdictCSXDefect, Count: 1},
			{Verdict: domain.AnomalyVerdictNotReproducible, Count: 1},
		},
	}, nil, anomalyNow)

	if view.DuplicateRate != "60.0%" {
		t.Fatalf("duplicate rate = %q, want 60.0%%", view.DuplicateRate)
	}
	if view.ConfirmedRate != "50.0%" {
		t.Fatalf("confirmed rate = %q; the denominator is decided reports, not submissions", view.ConfirmedRate)
	}
	if !view.FloodSuspected {
		t.Fatal("one bucket behind 8 of 10 submissions must be flagged")
	}
	if view.MeanLatency == "—" {
		t.Fatal("two decided reports must produce a mean")
	}
	if len(view.Verdicts) != 2 || !view.Verdicts[0].Confirmed || view.Verdicts[1].Confirmed {
		t.Fatalf("verdict rows do not mark promotability: %+v", view.Verdicts)
	}
}

// Work nobody can run must be visible as work nobody can run.
func TestAnomalyViewFlagsWorkNoLaneCanRun(t *testing.T) {
	view := buildAnomalyView(serverstore.AnomalyInsights{
		WindowStart: anomalyNow.AddDate(0, 0, -30), WindowEnd: anomalyNow,
		Reports: 3, Unique: 3, Unsupported: 3,
	}, []serverstore.AnomalyReportRow{{
		AnomalyType:       domain.AnomalyCSXFailLocalPass,
		PURL:              "pkg:pypi/httpx@0.27.0",
		Status:            domain.AnomalyStatusUnsupported,
		UnsupportedReason: "no verifier lane: the report names no sample this network published",
		Reports:           1,
		FirstSeen:         anomalyNow.Add(-90 * time.Minute),
	}}, anomalyNow)

	var unsupported anomalyStageView
	for _, stage := range view.Stages {
		if strings.Contains(stage.Label, "lane") {
			unsupported = stage
		}
	}
	if unsupported.Count != 3 || !unsupported.Attention {
		t.Fatalf("unsupported stage = %+v; it must not read as ordinary backlog", unsupported)
	}
	if len(view.Recent) != 1 || view.Recent[0].Reason == "" {
		t.Fatalf("the table gives the operator no reason to act on: %+v", view.Recent)
	}
	if view.Recent[0].Age == "—" {
		t.Fatal("a report with a first-seen time must show its age")
	}
}

// A panel that compiles is not a panel that shows. This renders the real
// dashboard with a real anomaly store behind it and reads the page.
type fakeAnomalyStore struct {
	insights serverstore.AnomalyInsights
	rows     []serverstore.AnomalyReportRow
	err      error
}

func (f *fakeAnomalyStore) RecordAnomalyReport(context.Context, serverstore.AnomalyReportRow, time.Time) (serverstore.AnomalyReportRow, bool, error) {
	return serverstore.AnomalyReportRow{}, false, nil
}
func (f *fakeAnomalyStore) AnomalyReportByID(context.Context, int64) (serverstore.AnomalyReportRow, bool, error) {
	return serverstore.AnomalyReportRow{}, false, nil
}
func (f *fakeAnomalyStore) AttachAnomalyVerificationJob(context.Context, int64, int64, string, string) error {
	return nil
}
func (f *fakeAnomalyStore) SetAnomalyVerdict(context.Context, int64, string, time.Time) (bool, error) {
	return false, nil
}
func (f *fakeAnomalyStore) OpenAnomalyReportsForSample(context.Context, string) ([]serverstore.AnomalyReportRow, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) ListAnomalyReports(context.Context, int) ([]serverstore.AnomalyReportRow, error) {
	return f.rows, f.err
}
func (f *fakeAnomalyStore) AnomalyInsights(context.Context, time.Time, int) (serverstore.AnomalyInsights, error) {
	return f.insights, f.err
}

func TestTheDashboardRendersTheAnomalyChannel(t *testing.T) {
	anomalies := &fakeAnomalyStore{
		insights: serverstore.AnomalyInsights{
			WindowStart: anomalyNow.AddDate(0, 0, -30), WindowEnd: anomalyNow,
			Reports: 5, Unique: 3, Duplicates: 2,
			Queued: 1, Verifying: 1, Verified: 1, Unsupported: 1,
			Confirmed: 1, CSXDefects: 1,
			VerdictLatencyTotal: time.Hour, VerdictLatencyCount: 1, VerdictLatencyMax: time.Hour,
			Verdicts: []serverstore.AnomalyVerdictCounts{{Verdict: domain.AnomalyVerdictCSXDefect, Count: 1}},
		},
		rows: []serverstore.AnomalyReportRow{{
			ID: 1, AnomalyType: domain.AnomalyCSXPassLocalFail,
			PURL: "pkg:npm/axios@1.12.0", Symbol: "axios.post",
			Status: domain.AnomalyStatusVerified, Verdict: domain.AnomalyVerdictCSXDefect,
			Reports: 2, JobID: 12, FirstSeen: anomalyNow.Add(-time.Hour),
		}},
	}
	mux, secret := configuredMuxWithAnomalies(t, &fakeStore{}, anomalies)
	body := anomalyPanelBody(t, mux, secret)

	for _, want := range []string{
		"이상 신고 채널", "report_anomaly",
		domain.AnomalyVerdictCSXDefect, "pkg:npm/axios@1.12.0",
		"검증 lane 없음", "신고 → 판정 소요",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the dashboard does not show %q", want)
		}
	}
}

// A store that cannot answer must say so rather than render a panel of zeros:
// "nobody reported anything" and "not measured here" are different facts.
func TestTheDashboardSaysWhenTheAnomalyChannelIsUnavailable(t *testing.T) {
	mux, secret := configuredMuxWithAnomalies(t, &fakeStore{}, nil)
	body := anomalyPanelBody(t, mux, secret)
	if !strings.Contains(body, "이상 신고 채널을 제공하지 않습니다") {
		t.Fatal("an absent channel rendered as something other than absent")
	}
}

func configuredMuxWithAnomalies(t *testing.T, store Store, anomalies serverstore.AnomalyStore) (*http.ServeMux, string) {
	t.Helper()
	secret := "a-long-random-admin-secret"
	mux := http.NewServeMux()
	sum := sha256.Sum256([]byte(secret))
	if !Register(mux, Deps{
		Store:       store,
		TokenSHA256: hex.EncodeToString(sum[:]),
		PublicURL:   "https://codesamplex.dev",
		Version:     "v1.2.3-test",
		StartedAt:   anomalyNow.Add(-time.Hour),
		Now:         func() time.Time { return anomalyNow },
		Anomalies:   anomalies,
	}) {
		t.Fatal("valid token hash did not register /admin")
	}
	return mux, secret
}

func anomalyPanelBody(t *testing.T, mux *http.ServeMux, secret string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin = %d", rec.Code)
	}
	return rec.Body.String()
}

// The flag was carried in the markup and styled nowhere, so a flagged number
// rendered exactly like an ordinary one. A screenshot caught it; this keeps it
// caught. Every class the page uses to say "look at this" must resolve to a
// rule, or the page is not saying it.
func TestTheAttentionMarkingOnAPlainMetricIsActuallyStyled(t *testing.T) {
	anomalies := &fakeAnomalyStore{insights: serverstore.AnomalyInsights{
		WindowStart: anomalyNow.AddDate(0, 0, -30), WindowEnd: anomalyNow,
		Reports: 20, Unique: 4, Duplicates: 16, Unsupported: 2, BusiestReporter: 18,
	}}
	mux, secret := configuredMuxWithAnomalies(t, &fakeStore{}, anomalies)
	body := anomalyPanelBody(t, mux, secret)

	if !strings.Contains(body, `class="metric needs-attention"`) {
		t.Fatal("a flooded window and unrunnable work produced no flagged metric at all")
	}
	if !strings.Contains(body, ".metric.needs-attention {") {
		t.Fatal("the page marks a metric for attention with a class the stylesheet does not define")
	}
}
