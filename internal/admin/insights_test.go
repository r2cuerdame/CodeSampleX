package admin

import (
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestInsightViewLeavesMissingDaysAsChartGaps(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	rows := []serverstore.AdminDailyStat{
		dailyStat("2026-08-14", 100, 10, 5),
		dailyStat("2026-08-15", 130, 12, 6),
		// August 16 is deliberately absent. August 17 must be a new segment,
		// not a zero point and not a two-day delta attributed to one day.
		dailyStat("2026-08-17", 190, 20, 8),
	}
	view := buildInsightView(serverstore.AdminInsights{Daily: rows}, serverstore.NetworkCounts{}, false, now)
	if view.SnapshotDays != 3 || view.MissingDays != 27 {
		t.Fatalf("snapshot coverage = %d/%d, want 3/30", view.SnapshotDays, view.MissingDays)
	}
	for _, trend := range view.Trends {
		if len(trend.Line.Dots) != 3 {
			t.Errorf("%s dots = %d, want exactly the 3 stored days", trend.Name, len(trend.Line.Dots))
		}
		if len(trend.Line.Lines) != 1 {
			t.Errorf("%s lines = %d, want only Aug 14→15", trend.Name, len(trend.Line.Lines))
		}
		if len(trend.Delta.Bars) != 1 || trend.Delta.ValidDays != 1 {
			t.Errorf("%s delta bars = %d valid=%d, want only Aug 15", trend.Name, len(trend.Delta.Bars), trend.Delta.ValidDays)
		}
	}
}

func TestTargetETARequiresThreeCompletedConsecutiveDeltas(t *testing.T) {
	today := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	rows := []serverstore.AdminDailyStat{
		dailyStat("2026-08-11", 1000, 100, 10),
		dailyStat("2026-08-12", 1100, 110, 11),
		dailyStat("2026-08-13", 1200, 130, 12),
		dailyStat("2026-08-14", 1300, 160, 13),
		// A partial current-day value is present but must not enter velocity.
		dailyStat("2026-08-17", 1400, 200, 14),
	}
	view := buildInsightView(serverstore.AdminInsights{Daily: rows}, serverstore.NetworkCounts{VerifiedSamples: 200}, true, today.Add(8*time.Hour))
	target := view.Target
	if !target.ETAAvailable || target.ValidDays != 3 || target.Average != "20.0" {
		t.Fatalf("ETA = available:%v days:%d average:%q reason:%q", target.ETAAvailable, target.ValidDays, target.Average, target.Reason)
	}
	if target.Remaining != 9800 || target.ETADays != 490 {
		t.Fatalf("target = remaining:%d ETA days:%d, want 9800 and 490", target.Remaining, target.ETADays)
	}

	short := buildInsightView(serverstore.AdminInsights{Daily: rows[:3]}, serverstore.NetworkCounts{VerifiedSamples: 130}, true, today.Add(8*time.Hour)).Target
	if short.ETAAvailable || short.ValidDays != 2 {
		t.Fatalf("two-day ETA = available:%v valid:%d, want unavailable with 2 valid days", short.ETAAvailable, short.ValidDays)
	}
}

func TestNegativeCumulativeChangeIsRenderedAsNetDecrease(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	rows := []serverstore.AdminDailyStat{
		dailyStat("2026-08-15", 100, 20, 10),
		dailyStat("2026-08-16", 90, 19, 9),
		dailyStat("2026-08-17", 110, 21, 11),
	}
	view := buildInsightView(serverstore.AdminInsights{Daily: rows}, serverstore.NetworkCounts{}, false, now)
	for _, trend := range view.Trends {
		if trend.Delta.ValidDays != 2 || len(trend.Delta.Bars) != 2 {
			t.Errorf("%s signed deltas = %+v, want both consecutive days", trend.Name, trend.Delta)
			continue
		}
		if trend.Delta.Bars[0].Value >= 0 || trend.Delta.Bars[0].Tone != "negative" {
			t.Errorf("%s decrease bar = %+v, want a negative bar", trend.Name, trend.Delta.Bars[0])
		}
		if trend.Delta.Bars[1].Value <= 0 || trend.Delta.Bars[1].Tone != "positive" {
			t.Errorf("%s increase bar = %+v, want a positive bar", trend.Name, trend.Delta.Bars[1])
		}
	}
}

func TestTargetETAUsesNetChangeIncludingDecreases(t *testing.T) {
	today := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	rows := []serverstore.AdminDailyStat{
		dailyStat("2026-08-11", 1000, 100, 10),
		dailyStat("2026-08-12", 1100, 130, 11),
		dailyStat("2026-08-13", 1200, 120, 12),
		dailyStat("2026-08-14", 1300, 140, 13),
	}
	target := buildTarget(rows, 140, true, today)
	if !target.ETAAvailable || target.ValidDays != 3 || target.Average != "13.3" {
		t.Fatalf("net ETA = available:%v days:%d average:%q reason:%q", target.ETAAvailable, target.ValidDays, target.Average, target.Reason)
	}
	if target.ETADays != 740 {
		t.Fatalf("net ETA days = %d, want 740", target.ETADays)
	}
}

func TestEcosystemLabelsDoNotPretendNPMSeparatesLanguages(t *testing.T) {
	rows := buildEcosystemMix([]serverstore.AdminEcosystemCount{
		{Ecosystem: "npm", Verifications: 9},
		{Ecosystem: "gem", Verifications: 1},
		{Ecosystem: "maven", Verifications: 2},
		{Ecosystem: "future", Verifications: 1},
	})
	if rows[0].Label != "npm · JavaScript/TypeScript" || rows[1].Label != "gem · Ruby" || rows[2].Label != "maven · Java/JVM" || rows[3].Label != "기타/알 수 없음" {
		t.Fatalf("ecosystem labels = %+v", rows)
	}
}

func dailyStat(day string, evidence, verified, packages int64) serverstore.AdminDailyStat {
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil {
		panic(err)
	}
	return serverstore.AdminDailyStat{
		Day:             parsed,
		Evidence:        serverstore.AdminMetricValue{Value: evidence, Valid: true},
		VerifiedSamples: serverstore.AdminMetricValue{Value: verified, Valid: true},
		Packages:        serverstore.AdminMetricValue{Value: packages, Valid: true},
	}
}
