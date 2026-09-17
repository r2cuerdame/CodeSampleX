package admin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/apidemand"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const (
	demandCountryLimit  = 10
	demandVersionLimit  = 10
	demandEndpointLimit = 12
)

// DemandReader is the collector seam the dashboard reads: whether demand
// is being recorded at all, whether the edge supplies a country, the
// bounded report and the collector's own loss counters.
type DemandReader interface {
	Available() bool
	CountryEnabled() bool
	Report(ctx context.Context, now time.Time) (apidemand.Report, error)
	Telemetry() apidemand.Telemetry
}

// demandView is the demand diagnostics panel. Every number is a bounded
// aggregate the store measured; an unavailable half says so instead of
// showing zeros.
type demandView struct {
	Available      bool
	Error          string
	CountryEnabled bool
	Since          string
	Telemetry      apidemand.Telemetry
	Undercounted   bool
	ServerRelease  string

	Days       []demandDayView
	DailyChart demandStackChart
	Week       demandWindowView
	PriorWeek  demandWindowView
	Deltas     []demandDelta

	Last24Hours demandStackChart
	HourOfDay   demandStackChart
	PeakHour    string

	Endpoints     []demandEndpointView
	EndpointsMore []demandEndpointView

	Countries      []demandCountryView
	CountriesMore  []demandCountryView
	CountryTotal   int64
	CountryUnknown int64
	CountryKnown   string

	Kinds           []demandKindView
	Versions        []demandVersionView
	VersionsMore    []demandVersionView
	Protocols       []demandKindView
	IdentifiedShare string
	StaleShare      string
	StaleRequests   int64
	CurrentRequests int64
	StaleKnown      bool

	PackagesAvailable bool
	PackagesError     string
	PackageWindow     string
	PackageTotal      int64
	PackageCount      int64
	Packages          []demandPackageView
	Gaps              []demandPackageView
	GapSamples        int64
	TopMisses         []demandCoordinateView

	SearchWeek     demandSearchWindowView
	SearchPrior    demandSearchWindowView
	SearchDeltas   []demandDelta
	SearchChart    demandStackChart
	SearchDays     []serverstore.AdminDemandSearchDay
	SearchObserved bool
}

type demandDayView struct {
	Day           string
	Observed      bool
	Total         int64
	Success       int64
	Failure       int64
	Rejected      int64
	Anonymous     int64
	Authenticated int64
	Unidentified  int64
	FailureRate   string
}

type demandWindowView struct {
	Total         int64
	Success       int64
	Failure       int64
	Rejected      int64
	Anonymous     int64
	Authenticated int64
	Unidentified  int64
	UniqueCallers int64
	SuccessRate   string
	FailureRate   string
	RejectedRate  string
}

// demandDelta is one week-over-week comparison. Tone follows the sign the
// operator wants: more calls is positive, more failures is negative.
type demandDelta struct {
	Label   string
	Current string
	Prior   string
	Delta   string
	Tone    string
}

type demandEndpointView struct {
	Route         string
	Calls         int64
	UniqueCallers int64
	Failure       int64
	Rejected      int64
	FailureRate   string
	RejectedRate  string
	P95           string
	Mean          string
	Slow          bool
	Failing       bool
}

type demandCountryView struct {
	Country  string
	Requests int64
	Share    string
	Width    float64
}

type demandKindView struct {
	Label    string
	Requests int64
	Share    string
}

type demandVersionView struct {
	Kind     string
	Version  string
	Protocol string
	Requests int64
	Share    string
	State    string
}

type demandPackageView struct {
	Ecosystem   string
	Name        string
	Impact      int64
	PriorImpact int64
	Delta       string
	Coordinates int64
	Samples     int64
	LastDay     string
	Gap         bool
}

type demandCoordinateView struct {
	Ecosystem string
	Name      string
	Version   string
	Symbol    string
	TargetOS  string
	Impact    int64
	LastDay   string
}

type demandSearchWindowView struct {
	Hits         int64
	Misses       int64
	Total        int64
	HitRate      string
	NoResultRate string
}

// demandStackChart is one server-rendered stacked bar chart: a fixed
// 600x128 viewBox, one bar per position, one segment per series.
type demandStackChart struct {
	Label  string
	Max    int64
	Bars   []demandStackBar
	Empty  bool
	Legend []demandLegend
}

type demandLegend struct {
	Class string
	Label string
}

type demandStackBar struct {
	Label    string
	Total    int64
	X        float64
	Width    float64
	Segments []demandStackSegment
	Observed bool
	Current  bool
}

type demandStackSegment struct {
	Class  string
	Y      float64
	Height float64
	Value  int64
	Title  string
}

// buildDemandView derives the whole panel. reader may be nil (nothing is
// collected), insights may be nil (no package/search read).
func (h *handler) buildDemandView(ctx context.Context, now time.Time) demandView {
	view := demandView{ServerRelease: h.releaseVersion, GapSamples: serverstore.AdminDemandGapSamples}
	switch {
	case h.demand == nil || !h.demand.Available():
		view.Error = "API 수요 텔레메트리 수집기가 구성되지 않았습니다"
	default:
		view.CountryEnabled = h.demand.CountryEnabled()
		view.Telemetry = h.demand.Telemetry()
		view.Undercounted = view.Telemetry.Dropped > 0 || view.Telemetry.StoreFailures > 0
		report, err := h.demand.Report(ctx, now)
		if err != nil {
			view.Error = "API 수요 텔레메트리를 불러올 수 없습니다"
		} else {
			view.Available = true
			fillDemandAPIView(&view, apidemand.Summarize(report, now, h.releaseVersion), now)
		}
	}
	if h.demandInsights == nil {
		view.PackagesError = "이 저장소는 패키지 수요 집계를 제공하지 않습니다"
	} else if insights, err := h.demandInsights.AdminDemand(ctx, now); err != nil {
		view.PackagesError = "패키지·검색 수요 집계를 불러올 수 없습니다"
	} else {
		view.PackagesAvailable = true
		fillDemandPackageView(&view, insights)
	}
	return view
}

func fillDemandAPIView(view *demandView, s apidemand.Summary, now time.Time) {
	if !s.OldestHour.IsZero() {
		view.Since = s.OldestHour.UTC().Format("2006-01-02 15:04 UTC")
	}
	view.Week = demandWindowViewOf(s.Week)
	view.PriorWeek = demandWindowViewOf(s.PriorWeek)
	view.Deltas = []demandDelta{
		demandDeltaOf("전체 호출", s.Week.Total, s.PriorWeek.Total, true),
		demandDeltaOf("성공", s.Week.Success, s.PriorWeek.Success, true),
		demandDeltaOf("실패 · 5xx", s.Week.Failure, s.PriorWeek.Failure, false),
		demandDeltaOf("거절 · 4xx", s.Week.Rejected, s.PriorWeek.Rejected, false),
		demandDeltaOf("고유 호출자", s.Week.UniqueCallers, s.PriorWeek.UniqueCallers, true),
		demandDeltaOf("익명 호출", s.Week.Anonymous, s.PriorWeek.Anonymous, true),
		demandDeltaOf("인증 호출", s.Week.Authenticated, s.PriorWeek.Authenticated, true),
	}

	days := make([]demandStackBar, 0, len(s.Days))
	for _, day := range s.Days {
		view.Days = append(view.Days, demandDayView{
			Day: day.Day, Observed: day.Observed, Total: day.Total, Success: day.Success, Failure: day.Failure,
			Rejected: day.Rejected, Anonymous: day.Anonymous, Authenticated: day.Authenticated, Unidentified: day.Unidentified,
			FailureRate: formatShare(day.Failure, day.Total),
		})
		days = append(days, demandStackBar{Label: day.Day, Observed: day.Observed, Current: day.Day == now.UTC().Format("2006-01-02"),
			Segments: demandOutcomeSegments(day.Success, day.Rejected, day.Failure, day.Day)})
	}
	view.DailyChart = buildDemandStackChart("최근 7일 · UTC 일별 API 호출", days, demandOutcomeLegend)

	hours := make([]demandStackBar, 0, 24)
	for _, hour := range s.Last24Hours {
		label := hour.Hour.Format("01-02 15:00")
		hours = append(hours, demandStackBar{Label: label, Observed: hour.Total > 0, Current: hour.Hour.Equal(now.UTC().Truncate(time.Hour)),
			Segments: demandOutcomeSegments(hour.Success, hour.Rejected, hour.Failure, label+" UTC")})
	}
	view.Last24Hours = buildDemandStackChart("최근 24시간 · UTC 시간별 API 호출", hours, demandOutcomeLegend)

	profile := make([]demandStackBar, 0, 24)
	var peak int
	for hour, total := range s.HourOfDay {
		if total > s.HourOfDay[peak] {
			peak = hour
		}
		label := fmt.Sprintf("%02d:00", hour)
		profile = append(profile, demandStackBar{Label: label, Observed: total > 0, Segments: []demandStackSegment{{Class: "seg-total", Value: total, Title: label + " UTC · " + formatInt(total) + "건"}}})
	}
	view.HourOfDay = buildDemandStackChart("최근 7일 · UTC 시간대별 합계", profile, nil)
	if s.Week.Total > 0 {
		view.PeakHour = fmt.Sprintf("%02d:00 UTC", peak)
	}

	for _, endpoint := range s.Endpoints {
		row := demandEndpointView{
			Route: endpoint.Route, Calls: endpoint.Calls, UniqueCallers: endpoint.UniqueCallers,
			Failure: endpoint.Failure, Rejected: endpoint.Rejected,
			FailureRate:  formatShare(endpoint.Failure, endpoint.Calls),
			RejectedRate: formatShare(endpoint.Rejected, endpoint.Calls),
			P95:          formatP95(endpoint.P95BoundMS, endpoint.P95Over, endpoint.Calls),
			Mean:         fmt.Sprintf("%d ms", endpoint.MeanMS),
			Slow:         endpoint.P95Over || endpoint.P95BoundMS >= 2500,
			Failing:      endpoint.FailureRate >= 0.05,
		}
		if len(view.Endpoints) < demandEndpointLimit {
			view.Endpoints = append(view.Endpoints, row)
		} else {
			view.EndpointsMore = append(view.EndpointsMore, row)
		}
	}

	view.CountryTotal = s.CountryTotal
	view.CountryUnknown = s.CountryUnknown
	view.CountryKnown = formatShare(s.CountryTotal-s.CountryUnknown, s.CountryTotal)
	var top int64
	if len(s.Countries) > 0 {
		top = s.Countries[0].Requests
	}
	for _, country := range s.Countries {
		row := demandCountryView{Country: country.Country, Requests: country.Requests, Share: formatShare(country.Requests, s.CountryTotal)}
		if top > 0 {
			row.Width = float64(country.Requests) / float64(top) * 100
		}
		if len(view.Countries) < demandCountryLimit {
			view.Countries = append(view.Countries, row)
		} else {
			view.CountriesMore = append(view.CountriesMore, row)
		}
	}

	var clientTotal int64
	for _, kind := range s.Kinds {
		clientTotal += kind.Requests
	}
	for _, kind := range s.Kinds {
		view.Kinds = append(view.Kinds, demandKindView{Label: demandKindLabel(kind.Kind), Requests: kind.Requests, Share: formatShare(kind.Requests, clientTotal)})
	}
	for _, protocol := range s.Protocols {
		label := protocol.Protocol
		if label == "" {
			label = "미표기"
		}
		view.Protocols = append(view.Protocols, demandKindView{Label: label, Requests: protocol.Requests, Share: fmt.Sprintf("%.1f%%", protocol.Share*100)})
	}
	for _, client := range s.Clients {
		row := demandVersionView{Kind: demandKindLabel(client.Kind), Version: client.Version, Protocol: client.Protocol, Requests: client.Requests, Share: formatShare(client.Requests, clientTotal)}
		switch {
		case client.Stale:
			row.State = "stale"
		case client.Current:
			row.State = "current"
		case client.Kind == apidemand.ClientCLI || client.Kind == apidemand.ClientMCP || client.Kind == apidemand.ClientDaemon || client.Kind == apidemand.ClientCSX:
			row.State = "unversioned"
		}
		if len(view.Versions) < demandVersionLimit {
			view.Versions = append(view.Versions, row)
		} else {
			view.VersionsMore = append(view.VersionsMore, row)
		}
	}
	view.IdentifiedShare = formatShare(s.Identified, clientTotal)
	view.StaleRequests = s.StaleRequests
	view.CurrentRequests = s.CurrentRequests
	view.StaleKnown = s.StaleRequests+s.CurrentRequests > 0
	view.StaleShare = formatShare(s.StaleRequests, s.StaleRequests+s.CurrentRequests)
}

func fillDemandPackageView(view *demandView, d serverstore.AdminDemand) {
	view.PackageWindow = fmt.Sprintf("UTC %s ~ %s", d.WindowStart, d.WindowEnd)
	view.PackageTotal = d.TotalImpact
	view.PackageCount = d.PackageCount
	for _, row := range d.Packages {
		view.Packages = append(view.Packages, demandPackageViewOf(row))
	}
	for _, row := range d.Gaps {
		view.Gaps = append(view.Gaps, demandPackageViewOf(row))
	}
	for _, row := range d.TopMisses {
		view.TopMisses = append(view.TopMisses, demandCoordinateView(row))
	}
	view.SearchWeek = demandSearchWindowViewOf(d.Week)
	view.SearchPrior = demandSearchWindowViewOf(d.PriorWeek)
	view.SearchDeltas = []demandDelta{
		demandDeltaOf("검색 결과 있음 · HIT", d.Week.Hits, d.PriorWeek.Hits, true),
		demandDeltaOf("검색 결과 없음 · NO_SAFE_MATCH", d.Week.Misses, d.PriorWeek.Misses, false),
		demandDeltaOf("검색 전체", d.Week.Total(), d.PriorWeek.Total(), true),
	}
	view.SearchDays = d.Search
	bars := make([]demandStackBar, 0, len(d.Search))
	for _, day := range d.Search {
		if day.Hits+day.Misses > 0 {
			view.SearchObserved = true
		}
		bars = append(bars, demandStackBar{Label: day.Day, Observed: day.Hits+day.Misses > 0, Current: day.Day == d.WindowEnd, Segments: []demandStackSegment{
			{Class: "seg-hit", Value: day.Hits, Title: day.Day + " · HIT " + formatInt(day.Hits) + "건"},
			{Class: "seg-miss", Value: day.Misses, Title: day.Day + " · NO_SAFE_MATCH " + formatInt(day.Misses) + "건"},
		}})
	}
	view.SearchChart = buildDemandStackChart("최근 14일 · UTC 일별 검색 결과", bars, []demandLegend{{"seg-hit", "결과 있음"}, {"seg-miss", "결과 없음"}})
}

func demandPackageViewOf(row serverstore.AdminDemandPackage) demandPackageView {
	return demandPackageView{
		Ecosystem: row.Ecosystem, Name: row.Name, Impact: row.Impact, PriorImpact: row.PriorImpact,
		Delta: signedCount(row.Impact - row.PriorImpact), Coordinates: row.Coordinates, Samples: row.Samples,
		LastDay: row.LastDay, Gap: row.Samples <= serverstore.AdminDemandGapSamples,
	}
}

func demandSearchWindowViewOf(w serverstore.AdminDemandSearchWindow) demandSearchWindowView {
	return demandSearchWindowView{Hits: w.Hits, Misses: w.Misses, Total: w.Total(), HitRate: formatShare(w.Hits, w.Total()), NoResultRate: formatShare(w.Misses, w.Total())}
}

func demandWindowViewOf(w apidemand.WindowTotals) demandWindowView {
	return demandWindowView{
		Total: w.Total, Success: w.Success, Failure: w.Failure, Rejected: w.Rejected,
		Anonymous: w.Anonymous, Authenticated: w.Authenticated, Unidentified: w.Unidentified, UniqueCallers: w.UniqueCallers,
		SuccessRate: formatShare(w.Success, w.Total), FailureRate: formatShare(w.Failure, w.Total), RejectedRate: formatShare(w.Rejected, w.Total),
	}
}

// demandDeltaOf compares two windows. A prior of zero has no percentage;
// the absolute change is still shown so a first week reads as new, not as
// infinite growth.
func demandDeltaOf(label string, current, prior int64, moreIsGood bool) demandDelta {
	delta := demandDelta{Label: label, Current: formatInt(current), Prior: formatInt(prior), Tone: "zero"}
	change := current - prior
	switch {
	case prior == 0 && current == 0:
		delta.Delta = "—"
	case prior == 0:
		delta.Delta = signedCount(change) + " · 이전 주 0"
	default:
		delta.Delta = fmt.Sprintf("%s · %+.1f%%", signedCount(change), float64(change)/float64(prior)*100)
	}
	if change > 0 {
		delta.Tone = "negative"
		if moreIsGood {
			delta.Tone = "positive"
		}
	} else if change < 0 {
		delta.Tone = "positive"
		if moreIsGood {
			delta.Tone = "negative"
		}
	}
	return delta
}

func formatP95(bound int64, over bool, calls int64) string {
	if calls == 0 {
		return "—"
	}
	if over {
		return fmt.Sprintf("> %d ms", bound)
	}
	return fmt.Sprintf("≤ %d ms", bound)
}

func demandKindLabel(kind string) string {
	switch kind {
	case apidemand.ClientCLI:
		return "CLI"
	case apidemand.ClientMCP:
		return "MCP"
	case apidemand.ClientDaemon:
		return "데몬"
	case apidemand.ClientCSX:
		return "csx · 기타 표면"
	case apidemand.ClientOther:
		return "외부 클라이언트"
	case apidemand.ClientNone:
		return "User-Agent 없음"
	}
	return kind
}

var demandOutcomeLegend = []demandLegend{{"seg-success", "성공 2xx·3xx"}, {"seg-rejected", "거절 4xx"}, {"seg-failure", "실패 5xx"}}

func demandOutcomeSegments(success, rejected, failure int64, label string) []demandStackSegment {
	return []demandStackSegment{
		{Class: "seg-success", Value: success, Title: label + " · 성공 " + formatInt(success) + "건"},
		{Class: "seg-rejected", Value: rejected, Title: label + " · 거절 " + formatInt(rejected) + "건"},
		{Class: "seg-failure", Value: failure, Title: label + " · 실패 " + formatInt(failure) + "건"},
	}
}

// buildDemandStackChart lays bars across a 600x128 viewBox with the
// baseline at y=112 and 96 px of height for the largest bar. Bars keep
// their order; a bar with no observation stays as a labelled gap.
func buildDemandStackChart(label string, bars []demandStackBar, legend []demandLegend) demandStackChart {
	chart := demandStackChart{Label: label, Bars: bars, Legend: legend}
	for i := range bars {
		var total int64
		for _, segment := range bars[i].Segments {
			total += segment.Value
		}
		bars[i].Total = total
		if total > chart.Max {
			chart.Max = total
		}
	}
	if len(bars) == 0 || chart.Max == 0 {
		chart.Empty = true
	}
	const left, right, baseline, height = 16.0, 584.0, 112.0, 96.0
	slot := (right - left) / float64(len(bars))
	width := slot * 0.72
	if slot > 40 {
		width = slot * 0.6
	}
	for i := range bars {
		bars[i].X = left + slot*float64(i) + (slot-width)/2
		bars[i].Width = width
		y := baseline
		for j := range bars[i].Segments {
			segment := &bars[i].Segments[j]
			if chart.Max == 0 || segment.Value <= 0 {
				segment.Height = 0
				segment.Y = y
				continue
			}
			segment.Height = float64(segment.Value) / float64(chart.Max) * height
			y -= segment.Height
			segment.Y = y
		}
	}
	return chart
}

// demandSourceLine is the disclosure a panel prints under its numbers. It
// is built once so the template cannot drift from what the collector does.
func demandSourceLine(view demandView) string {
	parts := []string{"수집 시작 " + orDash(view.Since)}
	if view.CountryEnabled {
		parts = append(parts, "국가는 에지 헤더 기준")
	} else {
		parts = append(parts, "국가 헤더 미구성")
	}
	parts = append(parts, fmt.Sprintf("대기 %d건 · 누락 %d건 · 저장 실패 %d건 · 플러시 %d회",
		view.Telemetry.Pending, view.Telemetry.Dropped, view.Telemetry.StoreFailures, view.Telemetry.Flushes))
	return strings.Join(parts, " · ")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// sortedDemandDays is a template helper guard: the day series is already
// oldest-first, but a defensive sort keeps a future store change from
// reversing the chart silently.
func sortedDemandDays(days []demandDayView) []demandDayView {
	out := append([]demandDayView(nil), days...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}
