package admin

import (
	"fmt"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type anonymousView struct {
	Metrics                          serverstore.AnonymousAnalytics
	Since, CredentialSince, From, To string
	CredentialRate                   string
	Charts, CredentialCharts         []anonymousChart
	CredentialRateChart              anonymousRateChart
	Daily                            []anonymousDailyView
	Cohorts                          []anonymousCohortView
	Retention                        []anonymousRetentionView
}
type anonymousChart struct {
	Name  string
	Max   int64
	Dots  []svgDot
	Lines []svgLine
}
type anonymousCohortView struct {
	Day   string
	Size  int64
	Cells []anonymousRetentionView
}
type anonymousRetentionView struct {
	Label, Rate  string
	Active, Size int64
	Height, Y    float64
	Eligible     bool
}
type anonymousDailyView struct {
	Day                                 string
	NRU, DAU, MAU                       int64
	CredentialPresent, CredentialIssued int64
	CredentialRate                      string
	CredentialCollected                 bool
}
type anonymousRateChart struct {
	Name  string
	Dots  []anonymousRateDot
	Lines []svgLine
}
type anonymousRateDot struct {
	X, Y      float64
	Day, Rate string
}

func buildAnonymousView(m serverstore.AnonymousAnalytics) anonymousView {
	v := anonymousView{Metrics: m}
	if !m.CollectedSince.IsZero() {
		v.Since = m.CollectedSince.UTC().Format("2006-01-02 15:04 UTC")
	}
	if !m.CredentialSince.IsZero() {
		v.CredentialSince = m.CredentialSince.UTC().Format("2006-01-02 15:04 UTC")
	}
	v.CredentialRate = formatShare(m.CredentialPresent, m.CredentialPresent+m.CredentialIssued)
	if n := len(m.Daily); n > 0 {
		v.From = m.Daily[0].Day.Format("2006-01-02")
		v.To = m.Daily[n-1].Day.Format("2006-01-02")
	}
	for idx, name := range []string{"NRU · 신규 익명 클라이언트", "DAU · 일간 익명 활성 사용자", "MAU · 최근 30일 익명 활성 사용자", "익명 활동 · 성공 API 요청"} {
		chart := anonymousChart{Name: name}
		for _, d := range m.Daily {
			value := []int64{d.NRU, d.DAU, d.MAU, d.Requests}[idx]
			if value > chart.Max {
				chart.Max = value
			}
		}
		for i, d := range m.Daily {
			value := []int64{d.NRU, d.DAU, d.MAU, d.Requests}[idx]
			x := 16.0
			if len(m.Daily) > 1 {
				x += 568 * float64(i) / float64(len(m.Daily)-1)
			}
			y := 110.0
			if chart.Max > 0 {
				y -= 94 * float64(value) / float64(chart.Max)
			}
			dot := svgDot{X: x, Y: y, Day: d.Day.Format("2006-01-02"), Value: value}
			if i > 0 {
				prev := chart.Dots[i-1]
				chart.Lines = append(chart.Lines, svgLine{X1: prev.X, Y1: prev.Y, X2: x, Y2: y})
			}
			chart.Dots = append(chart.Dots, dot)
		}
		v.Charts = append(v.Charts, chart)
	}
	credentialStart := anonymousViewDay(m.CredentialSince)
	var credentialDaily []serverstore.AnonymousDailyMetric
	for _, d := range m.Daily {
		collected := !credentialStart.IsZero() && !d.Day.Before(credentialStart)
		rate := "—"
		if collected {
			rate = formatShare(d.CredentialPresent, d.CredentialPresent+d.CredentialIssued)
			credentialDaily = append(credentialDaily, d)
		}
		v.Daily = append(v.Daily, anonymousDailyView{
			Day: d.Day.Format("2006-01-02"), NRU: d.NRU, DAU: d.DAU, MAU: d.MAU,
			CredentialPresent: d.CredentialPresent, CredentialIssued: d.CredentialIssued,
			CredentialRate: rate, CredentialCollected: collected,
		})
	}
	for idx, name := range []string{"X-CSX-Anonymous-ID 포함 요청", "서버 발급 요청 · 헤더 없음/무효"} {
		chart := anonymousChart{Name: name}
		for _, d := range credentialDaily {
			value := []int64{d.CredentialPresent, d.CredentialIssued}[idx]
			if value > chart.Max {
				chart.Max = value
			}
		}
		for i, d := range credentialDaily {
			value := []int64{d.CredentialPresent, d.CredentialIssued}[idx]
			x := anonymousChartX(i, len(credentialDaily))
			y := anonymousChartY(float64(value), float64(chart.Max))
			dot := svgDot{X: x, Y: y, Day: d.Day.Format("2006-01-02"), Value: value}
			if i > 0 {
				prev := chart.Dots[i-1]
				chart.Lines = append(chart.Lines, svgLine{X1: prev.X, Y1: prev.Y, X2: x, Y2: y})
			}
			chart.Dots = append(chart.Dots, dot)
		}
		v.CredentialCharts = append(v.CredentialCharts, chart)
	}
	v.CredentialRateChart.Name = "자격 증명 도입률 · 포함 / (포함 + 발급)"
	var previous *anonymousRateDot
	for i, d := range credentialDaily {
		total := d.CredentialPresent + d.CredentialIssued
		if total == 0 {
			previous = nil
			continue
		}
		rate := 100 * float64(d.CredentialPresent) / float64(total)
		dot := anonymousRateDot{X: anonymousChartX(i, len(credentialDaily)), Y: anonymousChartY(rate, 100), Day: d.Day.Format("2006-01-02"), Rate: fmt.Sprintf("%.1f%%", rate)}
		if previous != nil {
			v.CredentialRateChart.Lines = append(v.CredentialRateChart.Lines, svgLine{X1: previous.X, Y1: previous.Y, X2: dot.X, Y2: dot.Y})
		}
		v.CredentialRateChart.Dots = append(v.CredentialRateChart.Dots, dot)
		previous = &v.CredentialRateChart.Dots[len(v.CredentialRateChart.Dots)-1]
	}
	for _, offset := range []int{1, 7, 30} {
		v.Retention = append(v.Retention, anonymousRetentionView{Label: fmt.Sprintf("D%d", offset), Rate: "—"})
	}
	for _, c := range m.Cohorts {
		row := anonymousCohortView{Day: c.Day.Format("2006-01-02"), Size: c.Size}
		for i, r := range c.Retention {
			cell := anonymousRetentionView{Label: fmt.Sprintf("D%d", r.Day), Rate: "—", Active: r.Active, Size: c.Size, Eligible: r.Eligible}
			if r.Eligible && c.Size > 0 {
				cell.Height = 100 * float64(r.Active) / float64(c.Size)
				cell.Rate = fmt.Sprintf("%.1f%%", 100*float64(r.Active)/float64(c.Size))
				v.Retention[i].Active += r.Active
				v.Retention[i].Size += c.Size
			}
			row.Cells = append(row.Cells, cell)
		}
		v.Cohorts = append(v.Cohorts, row)
	}
	for i := range v.Retention {
		r := &v.Retention[i]
		if r.Size > 0 {
			r.Eligible = true
			r.Height = 94 * float64(r.Active) / float64(r.Size)
			r.Rate = fmt.Sprintf("%.1f%%", 100*float64(r.Active)/float64(r.Size))
		}
		r.Y = 110 - r.Height
	}
	return v
}

func anonymousViewDay(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func anonymousChartX(i, n int) float64 {
	if n <= 1 {
		return 16
	}
	return 16 + 568*float64(i)/float64(n-1)
}

func anonymousChartY(value, max float64) float64 {
	if max <= 0 {
		return 110
	}
	return 110 - 94*value/max
}
