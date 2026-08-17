package admin

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const verifiedSampleTarget int64 = 10_000

type insightView struct {
	WindowStart  string
	WindowEnd    string
	SnapshotDays int
	MissingDays  int
	Trends       []trendView
	Target       targetView
	Verification verificationView
	Ecosystems   []mixBar
	PackageDepth []serverstore.AdminPackageDepth
}

type trendView struct {
	Name        string
	Description string
	Latest      int64
	HasLatest   bool
	Line        linePlot
	Delta       barPlot
}

type linePlot struct {
	Lines []svgLine
	Dots  []svgDot
	Min   int64
	Max   int64
	Empty bool
}

type barPlot struct {
	Bars       []svgBar
	Min        int64
	Max        int64
	Baseline   float64
	RangeLabel string
	ValidDays  int
	Empty      bool
}

type svgLine struct{ X1, Y1, X2, Y2 float64 }

type svgDot struct {
	X, Y  float64
	Day   string
	Value int64
}

type svgBar struct {
	X, Y, Width, Height float64
	Day                 string
	Value               int64
	SignedValue         string
	Tone                string
}

type targetView struct {
	Target       int64
	Current      int64
	HasCurrent   bool
	Remaining    int64
	Progress     float64
	Achieved     bool
	ETAAvailable bool
	ETADays      int64
	ETADate      string
	Average      string
	ValidDays    int
	Reason       string
}

type verificationView struct {
	Total        int64
	Pass         int64
	Fail         int64
	Skipped      int64
	Unclassified int64
	Rows         []mixBar
}

type mixBar struct {
	Label string
	Value int64
	Share string
	Width float64
	Tone  string
}

type metricSelector func(serverstore.AdminDailyStat) serverstore.AdminMetricValue

func buildInsightView(raw serverstore.AdminInsights, current serverstore.NetworkCounts, countsAvailable bool, now time.Time) insightView {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -(serverstore.AdminInsightDays - 1))
	end := today.AddDate(0, 0, 1)

	daily := append([]serverstore.AdminDailyStat(nil), raw.Daily...)
	sort.Slice(daily, func(i, j int) bool { return daily[i].Day.Before(daily[j].Day) })
	seen := make(map[string]bool, serverstore.AdminInsightDays)
	for _, row := range daily {
		day := utcDay(row.Day)
		if !day.Before(start) && day.Before(end) {
			seen[day.Format("2006-01-02")] = true
		}
	}

	view := insightView{
		WindowStart:  start.Format("2006-01-02"),
		WindowEnd:    today.Format("2006-01-02"),
		SnapshotDays: len(seen),
		MissingDays:  serverstore.AdminInsightDays - len(seen),
		PackageDepth: raw.PackageDepth,
	}
	view.Trends = []trendView{
		buildTrend("증거(사용 관측)", "누적 관측과 실제 일별 순증감", daily, start, today, func(r serverstore.AdminDailyStat) serverstore.AdminMetricValue { return r.Evidence }),
		buildTrend("검증된 샘플", "계약 PASS 또는 이후 검증 상태", daily, start, today, func(r serverstore.AdminDailyStat) serverstore.AdminMetricValue { return r.VerifiedSamples }),
		buildTrend("공개 패키지", "레지스트리 공개 확인을 통과한 패키지", daily, start, today, func(r serverstore.AdminDailyStat) serverstore.AdminMetricValue { return r.Packages }),
	}

	currentVerified, hasCurrent := latestMetric(daily, func(r serverstore.AdminDailyStat) serverstore.AdminMetricValue { return r.VerifiedSamples })
	if countsAvailable {
		currentVerified, hasCurrent = current.VerifiedSamples, true
	}
	view.Target = buildTarget(daily, currentVerified, hasCurrent, today)
	view.Verification = buildVerification(raw.Verification)
	view.Ecosystems = buildEcosystemMix(raw.Ecosystems)
	return view
}

func buildTrend(name, description string, daily []serverstore.AdminDailyStat, start, today time.Time, selectMetric metricSelector) trendView {
	t := trendView{Name: name, Description: description}
	visible := make([]metricPoint, 0, serverstore.AdminInsightDays)
	for _, row := range daily {
		day := utcDay(row.Day)
		if day.Before(start) || day.After(today) {
			continue
		}
		metric := selectMetric(row)
		if !metric.Valid {
			continue
		}
		visible = append(visible, metricPoint{Day: day, Value: metric.Value})
	}
	if len(visible) > 0 {
		t.Latest, t.HasLatest = visible[len(visible)-1].Value, true
	}
	t.Line = buildLinePlot(visible, start)
	t.Delta = buildDeltaPlot(daily, start, today, selectMetric)
	return t
}

type metricPoint struct {
	Day   time.Time
	Value int64
}

func buildLinePlot(points []metricPoint, start time.Time) linePlot {
	plot := linePlot{Empty: len(points) == 0}
	if len(points) == 0 {
		return plot
	}
	plot.Min, plot.Max = points[0].Value, points[0].Value
	for _, point := range points[1:] {
		if point.Value < plot.Min {
			plot.Min = point.Value
		}
		if point.Value > plot.Max {
			plot.Max = point.Value
		}
	}
	for i, point := range points {
		x := chartX(dayDistance(start, point.Day))
		y := chartY(point.Value, plot.Min, plot.Max)
		plot.Dots = append(plot.Dots, svgDot{X: x, Y: y, Day: point.Day.Format("01-02"), Value: point.Value})
		if i > 0 && isNextDay(points[i-1].Day, point.Day) {
			prev := plot.Dots[len(plot.Dots)-2]
			plot.Lines = append(plot.Lines, svgLine{X1: prev.X, Y1: prev.Y, X2: x, Y2: y})
		}
	}
	return plot
}

func buildDeltaPlot(daily []serverstore.AdminDailyStat, start, today time.Time, selectMetric metricSelector) barPlot {
	var deltas []metricPoint
	for i := 1; i < len(daily); i++ {
		previousDay, day := utcDay(daily[i-1].Day), utcDay(daily[i].Day)
		if !isNextDay(previousDay, day) || day.Before(start) || day.After(today) {
			continue
		}
		previous, current := selectMetric(daily[i-1]), selectMetric(daily[i])
		if !previous.Valid || !current.Valid {
			continue
		}
		deltas = append(deltas, metricPoint{Day: day, Value: current.Value - previous.Value})
	}
	plot := barPlot{ValidDays: len(deltas), Empty: len(deltas) == 0}
	if len(deltas) == 0 {
		return plot
	}
	plot.Min, plot.Max = deltas[0].Value, deltas[0].Value
	for _, delta := range deltas[1:] {
		if delta.Value < plot.Min {
			plot.Min = delta.Value
		}
		if delta.Value > plot.Max {
			plot.Max = delta.Value
		}
	}
	plot.RangeLabel = fmt.Sprintf("최저 %s · 최고 %s", signedCount(plot.Min), signedCount(plot.Max))

	lower, upper := min(int64(0), plot.Min), max(int64(0), plot.Max)
	if lower == upper { // Every measured delta is zero.
		upper = 1
	}
	const chartTop, chartBottom = 16.0, 116.0
	chartHeight := chartBottom - chartTop
	valueRange := float64(upper - lower)
	plot.Baseline = chartTop + float64(upper)/valueRange*chartHeight
	for _, delta := range deltas {
		valueY := chartTop + float64(upper-delta.Value)/valueRange*chartHeight
		height := math.Abs(plot.Baseline - valueY)
		if height < 2 {
			height = 2
		}
		y := math.Min(plot.Baseline, valueY)
		tone := "positive"
		if delta.Value < 0 {
			tone = "negative"
		} else if delta.Value == 0 {
			tone = "zero"
			y = plot.Baseline - 1
		}
		plot.Bars = append(plot.Bars, svgBar{
			X: chartX(dayDistance(start, delta.Day)) - 5, Y: y,
			Width: 10, Height: height, Day: delta.Day.Format("01-02"), Value: delta.Value,
			SignedValue: signedCount(delta.Value), Tone: tone,
		})
	}
	return plot
}

func buildTarget(daily []serverstore.AdminDailyStat, current int64, hasCurrent bool, today time.Time) targetView {
	view := targetView{Target: verifiedSampleTarget, HasCurrent: hasCurrent}
	if !hasCurrent {
		view.Reason = "현재 검증 샘플 집계를 읽지 못했습니다."
		return view
	}
	view.Current = current
	if current >= verifiedSampleTarget {
		view.Achieved = true
		view.Progress = 100
		return view
	}
	view.Remaining = verifiedSampleTarget - current
	view.Progress = math.Max(0, math.Min(100, float64(current)/float64(verifiedSampleTarget)*100))

	var sum int64
	for i := 1; i < len(daily); i++ {
		previousDay, day := utcDay(daily[i-1].Day), utcDay(daily[i].Day)
		if !isNextDay(previousDay, day) || !day.Before(today) {
			continue // today's partial snapshot never depresses the run rate
		}
		previous, currentMetric := daily[i-1].VerifiedSamples, daily[i].VerifiedSamples
		if !previous.Valid || !currentMetric.Valid {
			continue
		}
		sum += currentMetric.Value - previous.Value
		view.ValidDays++
	}
	if view.ValidDays < 3 {
		view.Reason = "완료된 유효 일별 증가 구간이 3개 이상 쌓이면 표시합니다."
		return view
	}
	average := float64(sum) / float64(view.ValidDays)
	view.Average = fmt.Sprintf("%.1f", average)
	if average <= 0 {
		view.Reason = "유효 기간의 평균 순증가량이 0 이하라 도달일을 계산하지 않습니다."
		return view
	}
	view.ETADays = int64(math.Ceil(float64(view.Remaining) / average))
	view.ETADate = today.AddDate(0, 0, int(view.ETADays)).Format("2006-01-02")
	view.ETAAvailable = true
	return view
}

func buildVerification(counts serverstore.AdminVerificationCounts) verificationView {
	view := verificationView{
		Total: counts.Total(), Pass: counts.Pass, Fail: counts.Fail,
		Skipped: counts.Skipped, Unclassified: counts.Unclassified,
	}
	values := []struct {
		label string
		value int64
		tone  string
	}{
		{"PASS", counts.Pass, "pass"},
		{"FAIL", counts.Fail, "fail"},
		{"SKIPPED", counts.Skipped, "skip"},
	}
	if counts.Unclassified > 0 {
		values = append(values, struct {
			label string
			value int64
			tone  string
		}{"미분류", counts.Unclassified, "unknown"})
	}
	for _, item := range values {
		view.Rows = append(view.Rows, makeMixBar(item.label, item.value, view.Total, item.tone))
	}
	return view
}

func buildEcosystemMix(rows []serverstore.AdminEcosystemCount) []mixBar {
	var total int64
	for _, row := range rows {
		total += row.Verifications
	}
	out := make([]mixBar, 0, len(rows))
	for _, row := range rows {
		out = append(out, makeMixBar(ecosystemDisplayName(row.Ecosystem), row.Verifications, total, "ecosystem"))
	}
	return out
}

func ecosystemDisplayName(ecosystem string) string {
	labels := map[string]string{
		"npm":      "npm · JavaScript/TypeScript",
		"pypi":     "pypi · Python",
		"cargo":    "cargo · Rust",
		"golang":   "golang · Go",
		"gem":      "gem · Ruby",
		"composer": "composer · PHP",
		"pub":      "pub · Dart",
		"hex":      "hex · Elixir/Erlang",
		"other":    "기타/알 수 없음",
	}
	if label, ok := labels[ecosystem]; ok {
		return label
	}
	if ecosystem == "" {
		return "알 수 없음"
	}
	return "기타/알 수 없음"
}

func makeMixBar(label string, value, total int64, tone string) mixBar {
	width := 0.0
	if total > 0 {
		width = float64(value) / float64(total) * 100
	}
	return mixBar{Label: label, Value: value, Share: fmt.Sprintf("%.1f%%", width), Width: width, Tone: tone}
}

func latestMetric(daily []serverstore.AdminDailyStat, selectMetric metricSelector) (int64, bool) {
	for i := len(daily) - 1; i >= 0; i-- {
		if value := selectMetric(daily[i]); value.Valid {
			return value.Value, true
		}
	}
	return 0, false
}

func utcDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func isNextDay(previous, current time.Time) bool {
	return utcDay(previous).AddDate(0, 0, 1).Equal(utcDay(current))
}

func dayDistance(start, day time.Time) int {
	return int(utcDay(day).Sub(utcDay(start)).Hours() / 24)
}

func chartX(day int) float64 {
	return 16 + float64(day)*568/float64(serverstore.AdminInsightDays-1)
}

func chartY(value, min, max int64) float64 {
	if max == min {
		return 64
	}
	return 112 - float64(value-min)/float64(max-min)*96
}

func signedCount(value int64) string {
	if value > 0 {
		return fmt.Sprintf("+%d", value)
	}
	return fmt.Sprintf("%d", value)
}
