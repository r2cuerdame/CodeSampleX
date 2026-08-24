package admin

import (
	"fmt"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The operator view of the consumption-side feedback channel.
//
// The one number an operator has to be able to read here is not "how many
// reports" — a channel an LLM can write to will always have a number. It is
// how many of them turned out to be REAL, and how long that took to find out.
// A channel with a high confirmed ratio is telling us about our own defects;
// one with a high duplicate ratio and no confirmations is telling us about a
// client in a retry loop, and those want opposite responses.

// anomalyRecentLimit bounds the table on the page. It is a dashboard, not an
// export.
const anomalyRecentLimit = 25

type anomalyStageView struct {
	Label string
	Count int64
	// Attention marks the stage an operator should look at rather than read
	// past: work nobody can run.
	Attention bool
}

type anomalyVerdictRow struct {
	Verdict   string
	Count     int64
	Confirmed bool
}

type anomalyReportRow struct {
	Type        string
	Package     string
	Symbol      string
	Status      string
	Verdict     string
	Confirmed   bool
	Submissions int64
	Age         string
	Reason      string
	JobID       int64
}

type anomalyView struct {
	Available  bool
	RangeLabel string

	Reports       int64
	Unique        int64
	Duplicates    int64
	DuplicateRate string

	Stages []anomalyStageView

	Confirmed        int64
	ConfirmedRate    string
	CSXDefects       int64
	Insufficient     int64
	InsufficientRate string

	// MeanLatency and MaxLatency are "—" when nothing has reached a verdict.
	// A mean over nothing is absent, not zero, and 0ms on a channel that has
	// answered nothing is a number an operator would act on.
	MeanLatency string
	MaxLatency  string

	BusiestReporter int64
	// FloodSuspected marks one anonymous bucket accounting for most of the
	// window's submissions. It is a count, never a name.
	FloodSuspected bool

	Verdicts []anomalyVerdictRow
	Recent   []anomalyReportRow
}

func anomalyRate(part, whole int64) string {
	if whole <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", float64(part)/float64(whole)*100)
}

func buildAnomalyView(insights serverstore.AnomalyInsights, recent []serverstore.AnomalyReportRow, now time.Time) anomalyView {
	view := anomalyView{
		Available: true,
		RangeLabel: fmt.Sprintf("UTC %s~%s",
			insights.WindowStart.Format("2006-01-02"), insights.WindowEnd.Format("2006-01-02")),
		Reports:       insights.Reports,
		Unique:        insights.Unique,
		Duplicates:    insights.Duplicates,
		DuplicateRate: anomalyRate(insights.Duplicates, insights.Reports),
		Stages: []anomalyStageView{
			{Label: "대기열 진입", Count: insights.Queued},
			{Label: "검증기 claim", Count: insights.Verifying},
			{Label: "판정 완료", Count: insights.Verified},
			{Label: "검증 lane 없음", Count: insights.Unsupported, Attention: insights.Unsupported > 0},
		},
		Confirmed:        insights.Confirmed,
		ConfirmedRate:    anomalyRate(insights.Confirmed, insights.Verified),
		CSXDefects:       insights.CSXDefects,
		Insufficient:     insights.Insufficient,
		InsufficientRate: anomalyRate(insights.Insufficient, insights.Verified),
		MeanLatency:      "—",
		MaxLatency:       "—",
		BusiestReporter:  insights.BusiestReporter,
	}
	if mean, measured := insights.MeanVerdictLatency(); measured {
		view.MeanLatency = formatDuration(mean)
		view.MaxLatency = formatDuration(insights.VerdictLatencyMax)
	}
	// One bucket behind more than half the window's submissions, with more
	// than a handful of them, is what a runaway client looks like from here.
	if insights.Reports >= 10 && insights.BusiestReporter*2 > insights.Reports {
		view.FloodSuspected = true
	}
	for _, v := range insights.Verdicts {
		view.Verdicts = append(view.Verdicts, anomalyVerdictRow{
			Verdict:   v.Verdict,
			Count:     v.Count,
			Confirmed: domain.AnomalyVerdictConfirmed(v.Verdict),
		})
	}
	for i, row := range recent {
		if i >= anomalyRecentLimit {
			break
		}
		view.Recent = append(view.Recent, anomalyReportRow{
			Type:        row.AnomalyType,
			Package:     row.PURL,
			Symbol:      row.Symbol,
			Status:      row.Status,
			Verdict:     row.Verdict,
			Confirmed:   domain.AnomalyVerdictConfirmed(row.Verdict),
			Submissions: row.Reports,
			Age:         anomalyAge(row.FirstSeen, now),
			Reason:      strings.TrimSpace(row.UnsupportedReason),
			JobID:       row.JobID,
		})
	}
	return view
}

func anomalyAge(at, now time.Time) string {
	if at.IsZero() {
		return "—"
	}
	d := now.Sub(at)
	if d < 0 {
		d = 0
	}
	return formatDuration(d)
}
