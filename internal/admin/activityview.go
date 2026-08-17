package admin

import (
	"fmt"

	"github.com/r2cuerdame/codesamplex/internal/activity"
)

// activityDailyPlot is the 31-column daily chart of API activity IDs. Four
// column classes are kept apart on purpose: counted traffic, a health-proven
// zero, a collection gap, and a day before collection itself. Absence is never
// rendered as zero.
type activityDailyPlot struct {
	Bars         []svgBar
	HealthyZeros []svgMarker
	Gaps         []svgMarker
	Pending      []svgMarker
	Max          int64
	DayFrom      string
	DayTo        string
	StartEpoch   string
	GapDays      int
	Empty        bool
	Collecting   bool
	RangeNote    string
}

// svgMarker is a column with no measurement behind it, drawn as a short tick
// on the axis so a gap is visible rather than silently flat.
type svgMarker struct {
	X, Y, Width, Height float64
	Day                 string
	Label               string
}

func buildActivityDaily(window activity.DailyWindow) activityDailyPlot {
	plot := activityDailyPlot{
		Max: window.Max, GapDays: window.Gaps, StartEpoch: window.StartEpoch,
		Collecting: window.Collecting, Empty: true,
	}
	if len(window.Points) == 0 {
		plot.RangeNote = "수집된 UTC일이 없습니다"
		return plot
	}
	plot.DayFrom = window.Points[0].Epoch
	plot.DayTo = window.Points[len(window.Points)-1].Epoch

	step := 568.0
	if len(window.Points) > 1 {
		step /= float64(len(window.Points) - 1)
	}
	width := step * 0.58
	if width > 15 {
		width = 15
	}
	for i, point := range window.Points {
		x := 16 + float64(i)*step - width/2
		switch {
		case point.BeforeCollection:
			plot.Pending = append(plot.Pending, svgMarker{
				X: x, Y: 113, Width: width, Height: 3, Day: point.Epoch, Label: "수집 시작 전",
			})
		case point.Gap:
			plot.Gaps = append(plot.Gaps, svgMarker{
				X: x, Y: 108, Width: width, Height: 8, Day: point.Epoch, Label: "수집 공백",
			})
		case point.HealthyZero:
			plot.Empty = false
			plot.HealthyZeros = append(plot.HealthyZeros, svgMarker{
				X: x, Y: 110, Width: width, Height: 6, Day: point.Epoch, Label: "정상 수집 · API 활동 ID 0",
			})
		default:
			plot.Empty = false
			height := 2.0
			if window.Max > 0 {
				height = float64(point.Count) / float64(window.Max) * 94
				if height < 2 {
					height = 2
				}
			}
			plot.Bars = append(plot.Bars, svgBar{
				X: x, Y: 116 - height, Width: width, Height: height,
				Day: point.Epoch, Value: point.Count,
			})
		}
	}
	switch {
	case !window.Collecting:
		plot.RangeNote = "아직 수집된 API 활동 ID가 없습니다"
	case window.Gaps > 0:
		plot.RangeNote = fmt.Sprintf("수집 시작 %s · 수집 공백 %d일", window.StartEpoch, window.Gaps)
	default:
		plot.RangeNote = "수집 시작 " + window.StartEpoch + " · 수집 공백 없음"
	}
	return plot
}
