package admin

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The same instant configuredMuxFull hands the handler, so a rendered page
// shows the ages these fixtures actually describe rather than clamped zeros.
var flowNow = time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC)

func sampleFlow() serverstore.AdminFlow {
	return serverstore.AdminFlow{
		Hour: serverstore.AdminFlowWindow{
			Since: flowNow.Add(-time.Hour), Length: time.Hour,
			NoMatches: 1, Hits: 3,
			Verifications:   serverstore.AdminVerificationCounts{Pass: 4, Fail: 1},
			AcceptedSamples: 2, HeldSamples: 1,
		},
		Day: serverstore.AdminFlowWindow{
			Since: flowNow.Add(-24 * time.Hour), Length: 24 * time.Hour,
			NoMatches: 8, Hits: 24,
			Verifications:   serverstore.AdminVerificationCounts{Pass: 36, Fail: 8, Skipped: 4},
			AcceptedSamples: 24, HeldSamples: 3,
		},
		Week: serverstore.AdminFlowWindow{
			Since: flowNow.Add(-7 * 24 * time.Hour), Length: 7 * 24 * time.Hour,
			NoMatches: 40, Hits: 160,
			Verifications:   serverstore.AdminVerificationCounts{Pass: 300, Fail: 30, Skipped: 6},
			AcceptedSamples: 168, HeldSamples: 9,
		},
		PreviousHour: serverstore.AdminFlowWindow{
			Since: flowNow.Add(-2 * time.Hour), Length: time.Hour,
			NoMatches: 2, Hits: 6,
			Verifications:   serverstore.AdminVerificationCounts{Pass: 2},
			AcceptedSamples: 1,
		},
		LastVerification: flowNow.Add(-6 * time.Minute), HasLastVerification: true,
		LastSample: flowNow.Add(-11 * time.Minute), HasLastSample: true,
		LastSearchOutcome: flowNow.Add(-90 * time.Second), HasLastSearchOutcome: true,
	}
}

// The rate alone is unreadable: 25% of four searches and 25% of four hundred
// are different facts and only one of them is worth acting on. The issue's own
// example carries all three parts, and so does the card.
func TestFlowNoMatchShowsRateBasisAndWindowTogether(t *testing.T) {
	view := buildFlowView(sampleFlow(), serverstore.AdminJobQueue{}, flowNow)
	if !view.Available {
		t.Fatal("flow view is unavailable with data present")
	}
	card := view.NoMatch
	if card.Value != "25.0%" {
		t.Errorf("no-match value = %q, want 25.0%%", card.Value)
	}
	if card.Basis != "8 / 32" {
		t.Errorf("no-match basis = %q, want the numerator and denominator", card.Basis)
	}
	if card.Window != "최근 24시간" {
		t.Errorf("no-match window = %q", card.Window)
	}
}

// A window nobody searched in has no rate. Rendering 0% would report perfect
// search quality for a dead collector, which is the exact inversion this
// dashboard has already been burned by once.
func TestFlowNoMatchSaysNoSampleRatherThanZeroPercent(t *testing.T) {
	flow := sampleFlow()
	flow.Day.NoMatches, flow.Day.Hits = 0, 0
	view := buildFlowView(flow, serverstore.AdminJobQueue{}, flowNow)

	if view.NoMatch.Sampled {
		t.Fatal("an empty window reported a measured rate")
	}
	if view.NoMatch.Value != "—" {
		t.Errorf("no-match value = %q, want an em dash", view.NoMatch.Value)
	}
	if !strings.Contains(view.NoMatch.Basis, "표본 없음") {
		t.Errorf("no-match basis = %q, want 표본 없음", view.NoMatch.Basis)
	}
}

// A thin denominator is not an error, but a rate read off it is a guess. The
// count travels beside the percentage so the reader sees which one they have.
func TestFlowNoMatchKeepsASmallDenominatorVisible(t *testing.T) {
	flow := sampleFlow()
	flow.Day.NoMatches, flow.Day.Hits = 1, 1
	view := buildFlowView(flow, serverstore.AdminJobQueue{}, flowNow)

	if view.NoMatch.Value != "50.0%" {
		t.Errorf("no-match value = %q", view.NoMatch.Value)
	}
	if view.NoMatch.Basis != "1 / 2" {
		t.Errorf("no-match basis = %q, want 1 / 2", view.NoMatch.Basis)
	}
	if !view.NoMatch.ThinSample {
		t.Error("a two-search denominator was not marked thin")
	}
}

// Throughput is completed work. Counting only PASS reports a farm that is
// failing everything and a farm that stopped as the same number.
func TestFlowVerificationCountsEveryCompletedResult(t *testing.T) {
	view := buildFlowView(sampleFlow(), serverstore.AdminJobQueue{}, flowNow)

	if view.Verification.Value != "5" {
		t.Errorf("verifications this hour = %q, want 5 (4 PASS + 1 FAIL)", view.Verification.Value)
	}
	if view.Verification.Window != "최근 1시간" {
		t.Errorf("verification window = %q", view.Verification.Window)
	}
	// 48 completed in 24 hours is 2.0 per hour.
	if !strings.Contains(view.Verification.Support, "2.0") {
		t.Errorf("verification support = %q, want the 24h hourly average", view.Verification.Support)
	}
	// Two completed in the hour before this one, five in this one.
	if !strings.Contains(view.Verification.Support, "+3") {
		t.Errorf("verification support = %q, want the change against the previous hour", view.Verification.Support)
	}
}

// "The queue is full and throughput is zero" is the failure the operator most
// needs to see, and it is invisible in either number alone.
func TestFlowVerificationFlagsAFullQueueWithNoThroughput(t *testing.T) {
	flow := sampleFlow()
	flow.Hour.Verifications = serverstore.AdminVerificationCounts{}
	jobs := serverstore.AdminJobQueue{Cross: serverstore.AdminJobReasonCounts{Claimable: 42}}

	view := buildFlowView(flow, jobs, flowNow)
	if !view.Verification.Attention {
		t.Fatal("42 claimable jobs and no completed verification did not raise attention")
	}
	if !strings.Contains(view.Verification.Support, "42") {
		t.Errorf("verification support = %q, want the waiting job count named", view.Verification.Support)
	}
}

// An idle hour on a healthy farm and a stopped farm are the same zero. The
// last completed timestamp is the only thing that separates them.
func TestFlowVerificationZeroWithNoQueueIsNotAnAlarm(t *testing.T) {
	flow := sampleFlow()
	flow.Hour.Verifications = serverstore.AdminVerificationCounts{}

	view := buildFlowView(flow, serverstore.AdminJobQueue{}, flowNow)
	if view.Verification.Attention {
		t.Error("an idle hour with an empty queue was reported as a fault")
	}
	if !strings.Contains(view.LastVerification, "6분") {
		t.Errorf("last verification = %q, want the age of the last completed receipt", view.LastVerification)
	}
}

// Production is what became usable, not what was uploaded. The two are
// separate counts because a producer can be busy while nothing it makes ships.
func TestFlowSampleProductionSeparatesAcceptedFromHeld(t *testing.T) {
	view := buildFlowView(sampleFlow(), serverstore.AdminJobQueue{}, flowNow)

	if view.Sample.Value != "2" {
		t.Errorf("accepted samples this hour = %q, want 2", view.Sample.Value)
	}
	if !strings.Contains(view.Sample.Support, "1.0") {
		t.Errorf("sample support = %q, want the 24h hourly average", view.Sample.Support)
	}
	if view.Sample.Basis != "보류 1" {
		t.Errorf("sample basis = %q, want this hour's held count kept beside it, not merged in", view.Sample.Basis)
	}
}

// Nothing recorded is not "healthy a moment ago". With no timestamp at all the
// card has to say so rather than borrow the current time.
func TestFlowReportsNoActivityRatherThanInventingAnAge(t *testing.T) {
	view := buildFlowView(serverstore.AdminFlow{}, serverstore.AdminJobQueue{}, flowNow)

	for name, got := range map[string]string{
		"verification": view.LastVerification,
		"sample":       view.LastSample,
		"search":       view.LastSearchOutcome,
	} {
		if got != "기록 없음" {
			t.Errorf("last %s = %q, want 기록 없음", name, got)
		}
	}
	if view.Available {
		t.Error("an empty flow reported itself as available")
	}
}

// The three windows are the drill-down: one row each, every rate carrying the
// basis it was computed from.
func TestFlowRendersEveryWindowAsItsOwnRow(t *testing.T) {
	view := buildFlowView(sampleFlow(), serverstore.AdminJobQueue{}, flowNow)

	if len(view.Windows) != 3 {
		t.Fatalf("window rows = %d, want 3", len(view.Windows))
	}
	want := []string{"최근 1시간", "최근 24시간", "최근 7일"}
	for i, row := range view.Windows {
		if row.Window != want[i] {
			t.Errorf("row %d window = %q, want %q", i, row.Window, want[i])
		}
	}
	week := view.Windows[2]
	if week.NoMatch != "20.0%" {
		t.Errorf("7d no-match = %q, want 20.0%%", week.NoMatch)
	}
	if week.NoMatchBasis != "40 / 200" {
		t.Errorf("7d no-match basis = %q", week.NoMatchBasis)
	}
	// 336 completed over 168 hours is 2.0 per hour.
	if week.VerificationRate != "2.0/h" {
		t.Errorf("7d verification rate = %q, want 2.0/h", week.VerificationRate)
	}
	if !strings.Contains(week.VerificationMix, "PASS 300") || !strings.Contains(week.VerificationMix, "FAIL 30") {
		t.Errorf("7d verification mix = %q, want the completed result breakdown", week.VerificationMix)
	}
	if week.SampleRate != "1.0/h" {
		t.Errorf("7d sample rate = %q, want 1.0/h", week.SampleRate)
	}
}

// The headline No-match figure is one window; without the longer one beside it
// "25%" cannot be read as better or worse than usual, and a fixed alarm
// threshold would be a number nobody chose.
func TestFlowNoMatchCarriesTheWeekForComparison(t *testing.T) {
	view := buildFlowView(sampleFlow(), serverstore.AdminJobQueue{}, flowNow)
	if !strings.Contains(view.NoMatch.Support, "20.0%") {
		t.Errorf("no-match support = %q, want the 7-day rate for comparison", view.NoMatch.Support)
	}
	if !strings.Contains(view.NoMatch.Support, "최근 7일") {
		t.Errorf("no-match support = %q, want the comparison window named", view.NoMatch.Support)
	}
}

// The whole point of the panel is the first glance, so the three flow figures
// have to be in the summary the operator already looks at — not folded away
// with the read-only charts, and each one carrying the window it was measured
// over.
func TestDashboardLeadsWithTheThreeFlowKPIs(t *testing.T) {
	store := &fakeStore{
		insightsAvailable: true,
		insights: serverstore.AdminInsights{
			Jobs: serverstore.AdminJobQueue{Cross: serverstore.AdminJobReasonCounts{Claimable: 4}},
			Flow: sampleFlow(),
		},
	}
	mux, secret := configuredMux(t, store)
	rec := serve(mux, http.MethodGet, "/admin", "recuerdame", secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	summary := regexp.MustCompile(`(?s)<section class="panel priority-panel".*?</section>`).FindString(body)
	if summary == "" {
		t.Fatal("the operator summary panel is gone")
	}
	for _, want := range []string{
		"No match", "25.0%", "8 / 32", "최근 24시간",
		"검증 완료", "최근 1시간",
		"샘플 수용",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary panel missing flow figure %q", want)
		}
	}
	// Existing stock cards keep their place beside them.
	for _, kept := range []string{"가져갈 수 있는 검증 일감", "만료된 lease", "진행 중 검증"} {
		if !strings.Contains(summary, kept) {
			t.Errorf("stock card %q was displaced by the flow cards", kept)
		}
	}
	// And the per-window drill-down is on the page behind them.
	for _, want := range []string{"최근 7일", "20.0%", "40 / 200", "PASS 300"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing flow drill-down value %q", want)
		}
	}
}

// A dashboard with no flow data at all must say so rather than render a row of
// confident zeros — that reading is what sent an operator looking for a fault
// that was not there, and the reverse is worse.
func TestDashboardDoesNotRenderFlowZerosWithoutData(t *testing.T) {
	store := &fakeStore{insightsAvailable: true}
	mux, secret := configuredMux(t, store)
	rec := serve(mux, http.MethodGet, "/admin", "recuerdame", secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "아직 흐름 집계가 없습니다") {
		t.Error("an empty flow aggregate rendered without saying it is empty")
	}
	if strings.Contains(body, "0.0%") {
		t.Error("an unmeasured No-match rate rendered as a percentage")
	}
}

// A class name in the markup and no rule for it renders as an unstyled stack,
// and every structural assertion on this page still passes while it does —
// which is exactly how the flow cards shipped as a bare list once already. The
// stylesheet is inline, so it can be read the same way the markup is.
func TestFlowCardsHaveTheStyleTheirMarkupAsksFor(t *testing.T) {
	body := adminBody(t)
	style := regexp.MustCompile(`(?s)<style>.*?</style>`).FindString(body)
	if style == "" {
		t.Fatal("the page has no inline stylesheet")
	}
	for _, selector := range []string{
		".flow-grid {", ".flow-metric {", ".flow-metric .value {",
		".flow-metric .basis {", ".flow-metric .support {",
		".flow-metric.needs-attention {", ".flow-metric.unsampled .value {",
		".mono {",
	} {
		if !strings.Contains(style, selector) {
			t.Errorf("stylesheet has no rule for %q, so that markup renders unstyled", selector)
		}
	}
	// The cards carry four lines each; a fixed column count wraps them into
	// fragments on the panel widths this page actually uses.
	// A bare minmax floor forces its track wider than the panel on a small
	// phone and puts the whole page into a horizontal scroll.
	if !strings.Contains(style, "repeat(auto-fit,minmax(min(248px,100%),1fr))") {
		t.Error("the flow grid does not size itself to the panel it lands in")
	}
}
