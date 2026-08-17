package admin

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const accessChartDays = 31

type accessView struct {
	RangeLabel        string
	CollectionStarted string
	NoSource          bool
	PartialToday      bool
	SourceFiles       int
	TruncatedFiles    int
	MalformedLines    int64
	OversizeLines     int64
	QualityNote       string
	DaysWithRequests  int
	Total             AccessStatusCounts
	LatestDate        string
	LatestRequests    int64
	AveragePerDay     int64
	Daily             accessDailyPlot
	StatusRows        []mixBar
	GroupRows         []mixBar
	Routes            []accessRouteView
}

type accessDailyPlot struct {
	Bars    []svgBar
	Stacks  []accessStack
	Max     int64
	Empty   bool
	DayFrom string
	DayTo   string
}

type accessStack struct {
	X, Y, Width, Height float64
	Day, Label          string
	Value               int64
	Tone                string
}

type accessRouteView struct {
	Label      string
	GroupLabel string
	Requests   int64
	GetHead    int64
	Post       int64
	Other      int64
	Status429  int64
	Status5xx  int64
}

func buildAccessView(metrics AccessLogMetrics, now time.Time) accessView {
	now = now.UTC()
	view := accessView{
		SourceFiles: metrics.SourceFiles, TruncatedFiles: metrics.TruncatedFiles,
		MalformedLines: metrics.MalformedLines, OversizeLines: metrics.OversizeLines,
		DaysWithRequests: metrics.DaysWithRequests, Total: metrics.Totals,
		NoSource: metrics.SourceFiles == 0,
	}
	if !metrics.CollectionStartedAt.IsZero() {
		view.CollectionStarted = metrics.CollectionStartedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	if !metrics.OldestEventAt.IsZero() && !metrics.NewestEventAt.IsZero() {
		view.RangeLabel = metrics.OldestEventAt.UTC().Format("2006-01-02 15:04") + " ~ " + metrics.NewestEventAt.UTC().Format("2006-01-02 15:04 UTC")
		view.PartialToday = utcDay(metrics.NewestEventAt).Equal(utcDay(now))
	} else if view.NoSource {
		view.RangeLabel = "연결된 안전 로그 없음"
	} else {
		view.RangeLabel = "보존 범위를 확인할 이벤트 없음"
	}

	days := append([]AccessLogDay(nil), metrics.Days...)
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	if len(days) > accessChartDays {
		days = days[len(days)-accessChartDays:]
	}
	view.Daily = buildAccessDailyPlot(days)
	for i := len(days) - 1; i >= 0; i-- {
		if days[i].HasRequests {
			view.LatestDate, view.LatestRequests = days[i].Date, days[i].Requests
			break
		}
	}
	if metrics.DaysWithRequests > 0 {
		view.AveragePerDay = metrics.Totals.Requests / int64(metrics.DaysWithRequests)
	}

	view.StatusRows = []mixBar{
		makeMixBar("2xx", metrics.Totals.Status2xx, metrics.Totals.Requests, "pass"),
		makeMixBar("3xx", metrics.Totals.Status3xx, metrics.Totals.Requests, "coordinate"),
		makeMixBar("4xx (429 포함)", metrics.Totals.Status4xx, metrics.Totals.Requests, "skip"),
		makeMixBar("5xx", metrics.Totals.Status5xx, metrics.Totals.Requests, "fail"),
	}
	if metrics.Totals.StatusOther > 0 {
		view.StatusRows = append(view.StatusRows, makeMixBar("기타(1xx/비표준)", metrics.Totals.StatusOther, metrics.Totals.Requests, "unknown"))
	}
	for i, group := range metrics.Groups {
		view.GroupRows = append(view.GroupRows, makeMixBar(group.Label, group.Requests, metrics.Totals.Requests, accessTone(i)))
	}
	routes := make([]AccessRouteCounts, 0, len(metrics.Routes))
	for _, route := range metrics.Routes {
		if route.Requests > 0 {
			routes = append(routes, route)
		}
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Requests != routes[j].Requests {
			return routes[i].Requests > routes[j].Requests
		}
		return routes[i].Label < routes[j].Label
	})
	limit := len(routes)
	if limit > 10 {
		limit = 10
	}
	for _, route := range routes[:limit] {
		view.Routes = append(view.Routes, accessRouteView{
			Label: route.Label, GroupLabel: route.GroupLabel, Requests: route.Requests,
			GetHead: route.GetHeadRequests, Post: route.PostRequests, Other: route.OtherMethodRequests,
			Status429: route.Status429, Status5xx: route.Status5xx,
		})
	}
	view.QualityNote = accessQualityNote(view)
	return view
}

func buildAccessDailyPlot(days []AccessLogDay) accessDailyPlot {
	plot := accessDailyPlot{Empty: true}
	if len(days) == 0 {
		return plot
	}
	plot.DayFrom, plot.DayTo = days[0].Date, days[len(days)-1].Date
	for _, day := range days {
		if day.HasRequests && day.Requests > plot.Max {
			plot.Max = day.Requests
		}
	}
	if plot.Max == 0 {
		return plot
	}
	plot.Empty = false
	step := 568.0
	if len(days) > 1 {
		step /= float64(len(days) - 1)
	}
	width := step * 0.58
	if width > 15 {
		width = 15
	}
	for i, day := range days {
		if !day.HasRequests {
			continue
		}
		x := 16 + float64(i)*step
		height := float64(day.Requests) / float64(plot.Max) * 94
		if height < 2 {
			height = 2
		}
		plot.Bars = append(plot.Bars, svgBar{X: x - width/2, Y: 116 - height, Width: width, Height: height, Day: day.Date, Value: day.Requests})

		stackY := 116.0
		for groupIndex, group := range day.Groups {
			groupHeight := float64(group.Requests) / float64(plot.Max) * 94
			if groupHeight <= 0 {
				continue
			}
			stackY -= groupHeight
			plot.Stacks = append(plot.Stacks, accessStack{
				X: x - width/2, Y: stackY, Width: width, Height: groupHeight,
				Day: day.Date, Label: group.Label, Value: group.Requests, Tone: accessTone(groupIndex),
			})
		}
	}
	return plot
}

func accessTone(index int) string {
	switch index {
	case 0:
		return "consume"
	case 1:
		return "contribute"
	case 2:
		return "coordinate"
	default:
		return "unknown"
	}
}

func accessQualityNote(view accessView) string {
	parts := make([]string, 0, 3)
	if view.TruncatedFiles > 0 {
		parts = append(parts, fmt.Sprintf("크기 제한으로 일부만 읽은 파일 %d개", view.TruncatedFiles))
	}
	if view.MalformedLines > 0 {
		parts = append(parts, fmt.Sprintf("형식 오류 줄 %d개", view.MalformedLines))
	}
	if view.OversizeLines > 0 {
		parts = append(parts, fmt.Sprintf("크기 초과 줄 %d개", view.OversizeLines))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}
