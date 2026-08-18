package admin

import (
	"fmt"
	"math"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/activity"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const activityIDTarget int64 = 1_000

// growthView keeps the operator's requested growth questions together while
// preserving the boundary between measured proxies and unavailable user or
// search-outcome metrics. An unavailable card is deliberate evidence about
// the current instrumentation, not a zero.
type growthView struct {
	Metrics []growthMetric
	Proxies []growthMetric
}

type growthMetric struct {
	Name        string
	Value       string
	Description string
	Available   bool
	Tone        string
}

// buildGrowthView derives only metrics supported by the already-retained
// epoch-scoped activity aggregates. It never turns access-log requests,
// deduplicated Wanted coordinates, or adoption reports into search outcomes.
func buildGrowthView(metrics activity.Metrics, available bool, search serverstore.AdminSearchOutcomeCounts, now time.Time) growthView {
	sampleHit, noMatch := searchQualityMetrics(search)
	view := growthView{Metrics: []growthMetric{
		unavailableGrowth("사용자 증가 속도", "계정이나 고유 사용자 식별자를 저장하지 않습니다. API 활동 ID는 NAT·통신사망에서 여러 사람이 하나로 합쳐질 수 있어 사용자 증가율로 바꾸지 않습니다."),
		unavailableGrowth("재방문율", "서로 다른 UTC일의 활동 ID는 다른 HMAC 도메인으로 회전해 같은 사람의 재방문 여부를 연결할 수 없습니다. 아래에는 연결 없이 산출되는 재활동 ID-day 프록시만 표시합니다."),
		unavailableGrowth("MCP / CLI / API 반복 사용", "활동 저장소는 User-Agent, 클라이언트 종류, 경로를 보존하지 않습니다. 따라서 채널을 나누거나 같은 주체의 반복 호출을 판별할 수 없습니다."),
		sampleHit,
		unavailableGrowth("Finding hit rate", "/findings 검색의 결과 건수와 검색 이벤트를 저장하지 않습니다. 페이지 요청 수는 Finding HIT의 분모나 분자가 아닙니다."),
		noMatch,
		unavailableGrowth("사용자 1,000명까지의 기울기", "사용자 수 원천이 없으므로 사용자 1,000명 도달 속도나 날짜를 계산하지 않습니다. 아래 활동 ID 목표는 사용자 목표와 별개의 운영 프록시입니다."),
	}}

	if !available {
		view.Proxies = []growthMetric{
			unavailableGrowth("API 활동 ID 증가 기울기", "API 활동 ID 집계를 사용할 수 없습니다."),
			unavailableGrowth("일일 API 활동 ID 1,000 도달", "API 활동 ID 집계를 사용할 수 없습니다."),
			unavailableGrowth("재활동 ID-day 비율", "API 활동 ID 집계를 사용할 수 없습니다."),
		}
		return view
	}

	velocity, target := activityVelocity(metrics, now)
	repeat := activityRepeatShare(metrics, now)
	view.Proxies = []growthMetric{velocity, target, repeat}
	return view
}

func searchQualityMetrics(search serverstore.AdminSearchOutcomeCounts) (growthMetric, growthMetric) {
	if !search.Available || search.Total() == 0 {
		reason := "일별 검색 결과 집계가 아직 없습니다. 배포 후 성공한 /v1·/v2 검색 응답부터 UTC 날짜별 aggregate만 수집하며 과거 값을 추정해 채우지 않습니다."
		return unavailableGrowth("Sample hit rate · 공개 검색 API", reason), unavailableGrowth("No match 비율 · 공개 검색 API", reason)
	}
	total := search.Total()
	common := fmt.Sprintf("UTC %s~%s · 결과가 기록된 %d일 · 성공한 검색 응답 %d건", search.FirstDay, search.LastDay, search.Days, total)
	sampleRate := float64(search.SampleHits) / float64(total) * 100
	noMatchRate := float64(search.NoMatches) / float64(total) * 100
	return growthMetric{
			Name: "Sample hit rate · 공개 검색 API", Value: fmt.Sprintf("%.1f%%", sampleRate), Available: true, Tone: "positive",
			Description: fmt.Sprintf("%s 중 샘플을 한 개 이상 반환한 %d건의 비율입니다. query·패키지·심벌·사용자 ID는 이 집계에 저장하지 않으며 로컬 MCP/CLI 캐시 검색은 포함하지 않습니다.", common, search.SampleHits),
		}, growthMetric{
			Name: "No match 비율 · 공개 검색 API", Value: fmt.Sprintf("%.1f%%", noMatchRate), Available: true, Tone: "negative",
			Description: fmt.Sprintf("%s 중 NO_SAFE_MATCH를 반환한 %d건의 비율입니다. Wanted 대기열이나 HTTP 상태 코드로 역산하지 않으며 로컬 MCP/CLI 캐시 검색은 포함하지 않습니다.", common, search.NoMatches),
		}
}

func unavailableGrowth(name, reason string) growthMetric {
	return growthMetric{Name: name, Value: "데이터 없음", Description: reason, Tone: "unavailable"}
}

// activityVelocity uses the longest trailing run of completed, contiguous,
// measured UTC days. Today is excluded because it is still changing. Three
// points is the minimum needed before a slope is shown; collection gaps and
// pre-collection placeholders stop the run rather than becoming zeroes.
func activityVelocity(metrics activity.Metrics, now time.Time) (growthMetric, growthMetric) {
	name := "API 활동 ID 증가 기울기"
	targetName := "일일 API 활동 ID 1,000 도달"
	if metrics.Telemetry.Dropped > 0 || metrics.Telemetry.StoreFailures > 0 {
		reason := fmt.Sprintf("수집 누락 %d건·저장 실패 %d건이 있어 기울기를 계산하지 않습니다.", metrics.Telemetry.Dropped, metrics.Telemetry.StoreFailures)
		return unavailableGrowth(name, reason), unavailableGrowth(targetName, reason)
	}

	today := now.UTC().Format("2006-01-02")
	points := metrics.Daily.Points
	end := len(points) - 1
	for end >= 0 && points[end].Epoch >= today {
		end--
	}
	if end < 0 {
		reason := "완료된 UTC일 집계가 없습니다."
		return unavailableGrowth(name, reason), unavailableGrowth(targetName, reason)
	}
	start := end
	for start >= 0 {
		point := points[start]
		if point.BeforeCollection || point.Gap {
			break
		}
		start--
	}
	start++
	measured := points[start : end+1]
	if len(measured) < 3 {
		reason := fmt.Sprintf("연속된 완료 UTC일이 %d일뿐이라 최소 3일 기울기를 만들지 않습니다.", len(measured))
		return unavailableGrowth(name, reason), unavailableGrowth(targetName, reason)
	}

	slope := leastSquaresSlope(measured)
	velocity := growthMetric{
		Name: name, Value: fmt.Sprintf("%+.1f ID/일", slope), Available: true,
		Description: fmt.Sprintf("%s~%s의 연속된 완료 UTC %d일에 대한 최소제곱 직선 기울기입니다. 회전 네트워크 ID이며 사용자 수가 아닙니다.", measured[0].Epoch, measured[len(measured)-1].Epoch, len(measured)),
	}
	if slope > 0 {
		velocity.Tone = "positive"
	} else if slope < 0 {
		velocity.Tone = "negative"
	} else {
		velocity.Tone = "zero"
	}

	current := measured[len(measured)-1].Count
	target := growthMetric{Name: targetName}
	switch {
	case current >= activityIDTarget:
		target.Value, target.Available, target.Tone = "도달", true, "positive"
		target.Description = fmt.Sprintf("가장 최근 완료 UTC일 %s에 일일 API 활동 ID가 %d개였습니다. 사용자 1,000명 달성을 뜻하지 않습니다.", measured[len(measured)-1].Epoch, current)
	case slope <= 0:
		target = unavailableGrowth(targetName, fmt.Sprintf("현재 기울기 %+.1f ID/일이 양수가 아니어서 도달일을 외삽하지 않습니다.", slope))
	default:
		days := int64(math.Ceil(float64(activityIDTarget-current) / slope))
		lastDay, err := time.Parse("2006-01-02", measured[len(measured)-1].Epoch)
		if err != nil {
			target = unavailableGrowth(targetName, "최근 완료 UTC일을 해석할 수 없습니다.")
			break
		}
		target.Value, target.Available, target.Tone = fmt.Sprintf("약 %d일", days), true, "positive"
		target.Description = fmt.Sprintf("기울기가 그대로 유지된다는 단순 외삽은 %s UTC입니다. 계획이나 보장이 아니며 사용자 1,000명과는 다른 목표입니다.", lastDay.AddDate(0, 0, int(days)).Format("2006-01-02"))
	}
	return velocity, target
}

func leastSquaresSlope(points []activity.DayPoint) float64 {
	n := float64(len(points))
	var sumX, sumY, sumXY, sumXX float64
	for i, point := range points {
		x, y := float64(i), float64(point.Count)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denominator
}

// activityRepeatShare is the share of measured activity-ID days beyond each
// current-month ID's first observed day: (sum of daily distinct IDs - distinct
// monthly IDs) / sum of daily distinct IDs. It needs no cross-day join and
// therefore preserves the daily/monthly HMAC separation. It is not a person
// retention rate and is withheld when the monthly measurement window has a
// gap or collector loss.
func activityRepeatShare(metrics activity.Metrics, now time.Time) growthMetric {
	name := "재활동 ID-day 비율"
	if metrics.Telemetry.Dropped > 0 || metrics.Telemetry.StoreFailures > 0 || metrics.Telemetry.Pending > 0 {
		return unavailableGrowth(name, fmt.Sprintf("대기 %d건·수집 누락 %d건·저장 실패 %d건이 있어 비율을 계산하지 않습니다.", metrics.Telemetry.Pending, metrics.Telemetry.Dropped, metrics.Telemetry.StoreFailures))
	}
	monthStart := now.UTC().Format("2006-01") + "-01"
	start := monthStart
	if metrics.Daily.StartEpoch > start {
		start = metrics.Daily.StartEpoch
	}
	today := now.UTC().Format("2006-01-02")
	var sum int64
	measuredDays := 0
	for _, point := range metrics.Daily.Points {
		if point.Epoch < start || point.Epoch > today {
			continue
		}
		if point.BeforeCollection || point.Gap {
			return unavailableGrowth(name, fmt.Sprintf("%s~%s 측정 구간에 수집 공백이 있어 비율을 계산하지 않습니다.", start, today))
		}
		sum += point.Count
		measuredDays++
	}
	if measuredDays == 0 || sum == 0 {
		return unavailableGrowth(name, "현재 달 측정 구간에 API 활동 ID-day가 없습니다.")
	}
	if metrics.Counts.ExternalMAU > sum {
		return unavailableGrowth(name, "월 고유 활동 ID가 일별 활동 ID-day 합보다 커 집계 구간을 일치시킬 수 없습니다.")
	}
	repeated := sum - metrics.Counts.ExternalMAU
	share := float64(repeated) / float64(sum) * 100
	return growthMetric{
		Name: name, Value: fmt.Sprintf("%.1f%%", share), Available: true, Tone: "positive",
		Description: fmt.Sprintf("%s~%s의 일별 고유 ID 합 %d에서 월 고유 ID %d를 한 번씩 뺀 %d ID-day의 비중입니다. 사람 재방문율이 아니며 같은 ID를 날짜별로 연결하지 않습니다.", start, today, sum, metrics.Counts.ExternalMAU, repeated),
	}
}
