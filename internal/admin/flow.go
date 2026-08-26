package admin

import (
	"fmt"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// thinSearchSample is the denominator below which the rate above it is mostly
// noise. It does not withhold the number — withholding thin data is itself a
// judgement, and the operator can see the basis — it only marks it, so a
// two-search 50% is not read as the same fact as a two-hundred-search 50%.
const thinSearchSample = 20

// flowView is the production-flow half of the operator summary: three cards
// answering "is this line running", and one row per window behind them.
//
// Everything else on this page is stock. Stock cannot tell an operator that a
// verifier stopped forty minutes ago, because a corpus that stopped growing
// looks exactly like one that never was growing.
type flowView struct {
	Available bool

	NoMatch      flowCard
	Verification flowCard
	Sample       flowCard

	Windows []flowWindowView

	// Ages of the last recorded event of each kind. These are what make a
	// zero readable: the same "0 this hour" is a lull or a stopped lane
	// depending on them, and nothing else on the page distinguishes the two.
	LastVerification  string
	LastSample        string
	LastSearchOutcome string
}

type flowCard struct {
	Label  string
	Value  string
	Window string
	// Basis is the numbers the value was computed from — a rate that hides
	// its denominator is not evidence.
	Basis   string
	Support string
	// Sampled is false when the window had nothing to measure. The card then
	// shows an em dash: a measured zero and no measurement are different
	// facts, and one of them is a broken collector.
	Sampled bool
	// ThinSample marks a rate whose denominator is too small to steer by.
	ThinSample bool
	// Attention marks the one state the operator has to act on now.
	Attention bool
}

type flowWindowView struct {
	Window           string
	NoMatch          string
	NoMatchBasis     string
	Verifications    string
	VerificationRate string
	VerificationMix  string
	Samples          string
	SampleRate       string
	Held             string
}

func buildFlowView(flow serverstore.AdminFlow, jobs serverstore.AdminJobQueue, now time.Time) flowView {
	now = now.UTC()
	view := flowView{
		LastVerification:  flowAge(flow.LastVerification, flow.HasLastVerification, now),
		LastSample:        flowAge(flow.LastSample, flow.HasLastSample, now),
		LastSearchOutcome: flowAge(flow.LastSearchOutcome, flow.HasLastSearchOutcome, now),
	}
	view.Available = flow.HasLastVerification || flow.HasLastSample || flow.HasLastSearchOutcome ||
		flow.Week.SearchTotal() > 0 || flow.Week.Verifications.Total() > 0 ||
		flow.Week.AcceptedSamples > 0 || flow.Week.HeldSamples > 0

	view.NoMatch = buildNoMatchCard(flow)
	view.Verification = buildVerificationCard(flow, jobs)
	view.Sample = buildSampleCard(flow)

	for _, window := range []serverstore.AdminFlowWindow{flow.Hour, flow.Day, flow.Week} {
		view.Windows = append(view.Windows, buildFlowWindowView(window))
	}
	return view
}

// The headline No-match window is a day. An hour is the right window for
// throughput, where the producer is ours and runs continuously, and the wrong
// one for a rate, where the denominator is other people's searches and an
// hour of them is routinely single digits.
func buildNoMatchCard(flow serverstore.AdminFlow) flowCard {
	card := flowCard{Label: "No match", Window: flowWindowLabel(flow.Day.Length)}
	total := flow.Day.SearchTotal()
	if total == 0 {
		card.Value = "—"
		card.Basis = "표본 없음"
		card.Support = "이 창에 서버가 결과를 본 검색이 없습니다"
		return card
	}
	card.Sampled = true
	card.ThinSample = total < thinSearchSample
	card.Value = formatShare(flow.Day.NoMatches, total)
	card.Basis = fmt.Sprintf("%s / %s", formatInt(flow.Day.NoMatches), formatInt(total))

	// The longer window rather than a threshold. "25%" is not high or low on
	// its own, and a fixed alarm line would be a number nobody chose.
	if week := flow.Week.SearchTotal(); week > 0 {
		card.Support = fmt.Sprintf("%s %s · %s / %s",
			flowWindowLabel(flow.Week.Length), formatShare(flow.Week.NoMatches, week),
			formatInt(flow.Week.NoMatches), formatInt(week))
	} else {
		card.Support = fmt.Sprintf("%s 표본 없음", flowWindowLabel(flow.Week.Length))
	}
	return card
}

func buildVerificationCard(flow serverstore.AdminFlow, jobs serverstore.AdminJobQueue) flowCard {
	hour := flow.Hour.Verifications.Total()
	card := flowCard{
		Label: "검증 완료", Window: flowWindowLabel(flow.Hour.Length),
		Value: formatInt(hour), Sampled: true,
		Basis: flowVerificationMix(flow.Hour.Verifications),
	}
	support := fmt.Sprintf("%s 시간당 %s건 · 직전 1시간 대비 %s",
		flowWindowLabel(flow.Day.Length), formatHourlyAverage(flow.Day.Verifications.Total(), flow.Day.Length),
		signedCount(hour-flow.PreviousHour.Verifications.Total()))

	// The failure the operator most needs and neither number shows alone: work
	// is waiting and nothing is finishing it. An idle hour with an empty queue
	// is not the same event and is deliberately not flagged.
	if claimable := jobs.Claimable(); hour == 0 && claimable > 0 {
		card.Attention = true
		support = fmt.Sprintf("대기 중인 검증 일감 %s개가 있는데 완료된 검증이 없습니다 · %s",
			formatInt(claimable), support)
	}
	card.Support = support
	return card
}

func buildSampleCard(flow serverstore.AdminFlow) flowCard {
	card := flowCard{
		Label: "샘플 수용", Window: flowWindowLabel(flow.Hour.Length),
		Value: formatInt(flow.Hour.AcceptedSamples), Sampled: true,
		Basis: fmt.Sprintf("보류 %s", formatInt(flow.Hour.HeldSamples)),
	}
	card.Support = fmt.Sprintf("%s 시간당 %s건 · 보류 %s · 직전 1시간 대비 %s",
		flowWindowLabel(flow.Day.Length), formatHourlyAverage(flow.Day.AcceptedSamples, flow.Day.Length),
		formatInt(flow.Day.HeldSamples),
		signedCount(flow.Hour.AcceptedSamples-flow.PreviousHour.AcceptedSamples))
	return card
}

func buildFlowWindowView(window serverstore.AdminFlowWindow) flowWindowView {
	row := flowWindowView{
		Window:           flowWindowLabel(window.Length),
		NoMatch:          "—",
		NoMatchBasis:     "표본 없음",
		Verifications:    formatInt(window.Verifications.Total()),
		VerificationRate: formatPerHour(window.Verifications.Total(), window.Length),
		VerificationMix:  flowVerificationMix(window.Verifications),
		Samples:          formatInt(window.AcceptedSamples),
		SampleRate:       formatPerHour(window.AcceptedSamples, window.Length),
		Held:             formatInt(window.HeldSamples),
	}
	if total := window.SearchTotal(); total > 0 {
		row.NoMatch = formatShare(window.NoMatches, total)
		row.NoMatchBasis = fmt.Sprintf("%s / %s", formatInt(window.NoMatches), formatInt(total))
	}
	return row
}

func flowVerificationMix(counts serverstore.AdminVerificationCounts) string {
	mix := fmt.Sprintf("PASS %s · FAIL %s · SKIPPED %s",
		formatInt(counts.Pass), formatInt(counts.Fail), formatInt(counts.Skipped))
	if counts.Unclassified > 0 {
		mix += fmt.Sprintf(" · 미분류 %s", formatInt(counts.Unclassified))
	}
	return mix
}

func flowWindowLabel(length time.Duration) string {
	switch {
	case length >= 24*time.Hour && length%(24*time.Hour) == 0 && length > 24*time.Hour:
		return fmt.Sprintf("최근 %d일", int64(length/(24*time.Hour)))
	case length >= 24*time.Hour:
		return "최근 24시간"
	default:
		return fmt.Sprintf("최근 %d시간", int64(length/time.Hour))
	}
}

// flowAge never substitutes the current time for a missing one: an absent
// record means the collector never wrote, which is the finding, not a zero.
func flowAge(at time.Time, has bool, now time.Time) string {
	if !has {
		return "기록 없음"
	}
	return formatDuration(nonNegative(now.Sub(at))) + " 전"
}

func formatShare(part, total int64) string {
	if total <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", float64(part)/float64(total)*100)
}

func formatPerHour(count int64, length time.Duration) string {
	if length <= 0 {
		return "—"
	}
	return formatHourlyAverage(count, length) + "/h"
}

func formatHourlyAverage(count int64, length time.Duration) string {
	hours := length.Hours()
	if hours <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f", float64(count)/hours)
}
