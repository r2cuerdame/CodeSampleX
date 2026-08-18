package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/activity"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestGrowthViewUsesOnlyMeasuredActivityProxies(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	daily := []activity.DayCount{
		{Epoch: "2026-08-13", Count: 10, Rows: 10, Healthy: true},
		{Epoch: "2026-08-14", Count: 15, Rows: 15, Healthy: true},
		{Epoch: "2026-08-15", Count: 20, Rows: 20, Healthy: true},
		{Epoch: "2026-08-16", Count: 25, Rows: 25, Healthy: true},
		{Epoch: "2026-08-17", Count: 30, Rows: 30, Healthy: true},
		{Epoch: "2026-08-18", Count: 35, Rows: 35, Healthy: true},
	}
	metrics := activity.Metrics{
		Counts: activity.Counts{ExternalDAU: 35, ExternalMAU: 60, DaySeen: true, MonthSeen: true},
		Daily:  activity.BuildDailyWindow(now, activity.DailyRaw{OldestEpoch: "2026-08-13", Days: daily}),
	}
	view := buildGrowthView(metrics, true, serverstore.AdminSearchOutcomeCounts{}, now)

	if len(view.Metrics) != 7 || len(view.Proxies) != 3 {
		t.Fatalf("growth cards = requested:%d proxies:%d", len(view.Metrics), len(view.Proxies))
	}
	for _, card := range view.Metrics {
		if card.Available || card.Value != "데이터 없음" || card.Description == "" {
			t.Errorf("requested metric invented a value: %+v", card)
		}
	}
	if got := view.Proxies[0]; !got.Available || got.Value != "+5.0 ID/일" || !strings.Contains(got.Description, "완료 UTC 5일") || !strings.Contains(got.Description, "사용자 수가 아닙니다") {
		t.Fatalf("velocity = %+v", got)
	}
	if got := view.Proxies[1]; !got.Available || got.Value != "약 194일" || !strings.Contains(got.Description, "2027-02-27 UTC") || !strings.Contains(got.Description, "사용자 1,000명과는 다른") {
		t.Fatalf("target proxy = %+v", got)
	}
	if got := view.Proxies[2]; !got.Available || got.Value != "55.6%" || !strings.Contains(got.Description, "일별 고유 ID 합 135") || !strings.Contains(got.Description, "월 고유 ID 60") || !strings.Contains(got.Description, "같은 ID를 날짜별로 연결하지 않습니다") {
		t.Fatalf("repeat proxy = %+v", got)
	}
}

func TestGrowthViewWithholdsProxiesAcrossCollectionGapsOrLoss(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	metrics := activity.Metrics{
		Counts: activity.Counts{ExternalMAU: 10},
		Daily: activity.BuildDailyWindow(now, activity.DailyRaw{
			OldestEpoch: "2026-08-14",
			Days: []activity.DayCount{
				{Epoch: "2026-08-14", Count: 4, Rows: 4, Healthy: true},
				{Epoch: "2026-08-15", Count: 5, Rows: 5, Healthy: true},
				// 16 is a collection gap; only 17 is a completed trailing day.
				{Epoch: "2026-08-17", Count: 6, Rows: 6, Healthy: true},
				{Epoch: "2026-08-18", Count: 7, Rows: 7, Healthy: true},
			},
		}),
	}
	view := buildGrowthView(metrics, true, serverstore.AdminSearchOutcomeCounts{}, now)
	if view.Proxies[0].Available || !strings.Contains(view.Proxies[0].Description, "최소 3일") {
		t.Fatalf("gap velocity = %+v", view.Proxies[0])
	}
	if view.Proxies[2].Available || !strings.Contains(view.Proxies[2].Description, "수집 공백") {
		t.Fatalf("gap repeat share = %+v", view.Proxies[2])
	}

	metrics.Telemetry = activity.Telemetry{Dropped: 2, StoreFailures: 1, Pending: 3}
	view = buildGrowthView(metrics, true, serverstore.AdminSearchOutcomeCounts{}, now)
	if view.Proxies[0].Available || !strings.Contains(view.Proxies[0].Description, "수집 누락 2건") {
		t.Fatalf("loss velocity = %+v", view.Proxies[0])
	}
	if view.Proxies[2].Available || !strings.Contains(view.Proxies[2].Description, "대기 3건") {
		t.Fatalf("loss repeat share = %+v", view.Proxies[2])
	}
}

func TestGrowthViewUnavailableDoesNotInventZeros(t *testing.T) {
	view := buildGrowthView(activity.Metrics{}, false, serverstore.AdminSearchOutcomeCounts{}, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC))
	for _, card := range append(view.Metrics, view.Proxies...) {
		if card.Available || card.Value != "데이터 없음" {
			t.Errorf("unavailable source produced value: %+v", card)
		}
	}
}

func TestDashboardNamesRequestedMetricsAndTheirRealSources(t *testing.T) {
	mux, secret := configuredMux(t, &fakeStore{})
	body := serve(mux, "GET", "/admin", "admin", secret).Body.String()
	for _, want := range []string{
		"성장·재사용·검색 품질", "사용자 증가 속도", "재방문율", "MCP / CLI / API 반복 사용",
		"Sample hit rate", "Finding hit rate", "No match 비율", "사용자 1,000명까지의 기울기",
		"실제 저장 원천으로 계산되는 값만 표시합니다", "과거 값을 추정해 채우지 않습니다",
		"일별 검색 결과 집계가 아직 없습니다", "데이터 없음",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestSearchQualityMetricsUseRecordedOutcomeDenominator(t *testing.T) {
	search := serverstore.AdminSearchOutcomeCounts{
		Available: true, SampleHits: 75, NoMatches: 25, Days: 4,
		FirstDay: "2026-08-15", LastDay: "2026-08-18",
	}
	sample, noMatch := searchQualityMetrics(search)
	if !sample.Available || sample.Value != "75.0%" || !strings.Contains(sample.Description, "샘플을 한 개 이상 반환한 75건") || !strings.Contains(sample.Description, "성공한 검색 응답 100건") || !strings.Contains(sample.Description, "로컬 MCP/CLI 캐시 검색은 포함하지 않습니다") {
		t.Fatalf("sample hit = %+v", sample)
	}
	if !noMatch.Available || noMatch.Value != "25.0%" || !strings.Contains(noMatch.Description, "NO_SAFE_MATCH를 반환한 25건") || !strings.Contains(noMatch.Description, "Wanted 대기열이나 HTTP 상태 코드로 역산하지 않으며") || !strings.Contains(noMatch.Description, "로컬 MCP/CLI 캐시 검색은 포함하지 않습니다") {
		t.Fatalf("no match = %+v", noMatch)
	}
}
