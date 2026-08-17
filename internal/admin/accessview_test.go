package admin

import (
	"strings"
	"testing"
	"time"
)

func TestAccessViewUsesObservedDaysAndActualRetentionBoundary(t *testing.T) {
	now := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)
	groups := func(a, b, c int64) []AccessGroupCounts {
		return []AccessGroupCounts{
			{Label: "검색·전달", AccessStatusCounts: AccessStatusCounts{Requests: a}},
			{Label: "피드백·기여", AccessStatusCounts: AccessStatusCounts{Requests: b}},
			{Label: "조정·메타데이터", AccessStatusCounts: AccessStatusCounts{Requests: c}},
		}
	}
	metrics := AccessLogMetrics{
		Days: []AccessLogDay{
			{Date: "2026-08-15", HasRequests: true, AccessStatusCounts: AccessStatusCounts{Requests: 10}, Groups: groups(6, 3, 1)},
			// No request bar is invented for a day without retained calls.
			{Date: "2026-08-16", HasRequests: false},
			{Date: "2026-08-17", HasRequests: true, AccessStatusCounts: AccessStatusCounts{Requests: 20}, Groups: groups(12, 5, 3)},
		},
		Routes: []AccessRouteCounts{
			{Label: "적은 호출", GroupLabel: "조정", AccessStatusCounts: AccessStatusCounts{Requests: 2}},
			{Label: "많은 호출", GroupLabel: "POST 기여 · 기타 조정", AccessStatusCounts: AccessStatusCounts{Requests: 28, GetHeadRequests: 8, PostRequests: 17, OtherMethodRequests: 3}},
			{Label: "호출 없음", GroupLabel: "조정"},
		},
		Groups: []AccessGroupCounts{
			{Label: "검색·전달", AccessStatusCounts: AccessStatusCounts{Requests: 18}},
			{Label: "피드백·기여", AccessStatusCounts: AccessStatusCounts{Requests: 8}},
			{Label: "조정·메타데이터", AccessStatusCounts: AccessStatusCounts{Requests: 4}},
		},
		Totals:              AccessStatusCounts{Requests: 30, GetHeadRequests: 24, PostRequests: 6, Status2xx: 24, Status3xx: 1, Status4xx: 4, Status429: 1, Status5xx: 1},
		CollectionStartedAt: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
		OldestEventAt:       time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC),
		NewestEventAt:       time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC),
		DaysWithRequests:    2, SourceFiles: 2, TruncatedFiles: 1, MalformedLines: 2,
	}
	view := buildAccessView(metrics, now)
	if len(view.Daily.Bars) != 2 || len(view.Daily.Stacks) != 6 {
		t.Fatalf("daily plot = %d bars, %d stacks; want only 2 observed days and 6 group segments", len(view.Daily.Bars), len(view.Daily.Stacks))
	}
	if view.RangeLabel != "2026-08-15 03:00 ~ 2026-08-17 12:30 UTC" || !view.PartialToday {
		t.Fatalf("retention = %q partial=%v", view.RangeLabel, view.PartialToday)
	}
	if view.LatestDate != "2026-08-17" || view.LatestRequests != 20 || view.AveragePerDay != 15 {
		t.Fatalf("latest/average = %s %d %d", view.LatestDate, view.LatestRequests, view.AveragePerDay)
	}
	if len(view.Routes) != 2 || view.Routes[0].Label != "많은 호출" || view.Routes[0].GroupLabel != "POST 기여 · 기타 조정" || view.Routes[0].GetHead != 8 || view.Routes[0].Post != 17 || view.Routes[0].Other != 3 {
		t.Fatalf("routes were not ranked by requests: %+v", view.Routes)
	}
	if route := view.Routes[0]; route.GetHead+route.Post+route.Other != route.Requests {
		t.Fatalf("route method buckets do not reconcile: %+v", route)
	}
	if len(view.StatusRows) != 4 || view.StatusRows[1].Label != "3xx" {
		t.Fatalf("status rows = %+v, want disjoint 2xx/3xx/4xx/5xx", view.StatusRows)
	}
	if !strings.Contains(view.QualityNote, "일부만 읽은 파일 1개") || !strings.Contains(view.QualityNote, "형식 오류 줄 2개") {
		t.Fatalf("quality note = %q", view.QualityNote)
	}
}

func TestAccessViewDoesNotCallMissingSourceZeroTraffic(t *testing.T) {
	view := buildAccessView(AccessLogMetrics{Days: make([]AccessLogDay, 31)}, time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC))
	if !view.NoSource || view.RangeLabel != "연결된 안전 로그 없음" || !view.Daily.Empty {
		t.Fatalf("empty source = %+v", view)
	}
}
