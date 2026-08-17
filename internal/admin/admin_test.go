package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/activity"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type fakeStore struct {
	statsErr          error
	counts            serverstore.NetworkCounts
	countsErr         error
	wanted            []serverstore.WantedRow
	wantedTotal       int
	wantedErr         error
	adoption          serverstore.AdoptionCounts
	adoptionErr       error
	insights          serverstore.AdminInsights
	insightsAvailable bool
	insightsErr       error

	statsCalls    int
	countsCalls   int
	wantedCalls   int
	adoptionCalls int
	insightsCalls int
	wantedQuery   string
	wantedOffset  int
	wantedLimit   int
}

type fakeAccessReader struct {
	metrics AccessLogMetrics
	err     error
	calls   int
}

type fakeActivityReader struct {
	metrics     activity.Metrics
	metricsErr  error
	markErr     error
	markCalls   int
	metricCalls int
}

func (f *fakeActivityReader) MarkOwner(context.Context, *http.Request, time.Time) error {
	f.markCalls++
	return f.markErr
}

func (f *fakeActivityReader) Metrics(context.Context, time.Time) (activity.Metrics, error) {
	f.metricCalls++
	return f.metrics, f.metricsErr
}

func (f *fakeActivityReader) Telemetry() activity.Telemetry { return f.metrics.Telemetry }

func (f *fakeAccessReader) Metrics(context.Context, time.Time) (AccessLogMetrics, error) {
	f.calls++
	return f.metrics, f.err
}

func (f *fakeStore) GetLatestStats(context.Context) (string, bool, error) {
	f.statsCalls++
	return `{}`, true, f.statsErr
}

func (f *fakeStore) NetworkCounts(context.Context, time.Time) (serverstore.NetworkCounts, error) {
	f.countsCalls++
	return f.counts, f.countsErr
}

func (f *fakeStore) ListWanted(_ context.Context, query string, offset, limit int) ([]serverstore.WantedRow, int, error) {
	f.wantedCalls++
	f.wantedQuery, f.wantedOffset, f.wantedLimit = query, offset, limit
	return f.wanted, f.wantedTotal, f.wantedErr
}

func (f *fakeStore) AdoptionSummary(context.Context) (serverstore.AdoptionCounts, error) {
	f.adoptionCalls++
	return f.adoption, f.adoptionErr
}

func (f *fakeStore) AdminInsights(context.Context, time.Time) (serverstore.AdminInsights, bool, error) {
	f.insightsCalls++
	return f.insights, f.insightsAvailable, f.insightsErr
}

func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func configuredMux(t *testing.T, store Store) (*http.ServeMux, string) {
	return configuredMuxWithAccess(t, store, nil)
}

func configuredMuxWithAccess(t *testing.T, store Store, access AccessMetricsReader) (*http.ServeMux, string) {
	return configuredMuxFull(t, store, access, nil)
}

func configuredMuxFull(t *testing.T, store Store, access AccessMetricsReader, activityReader ActivityReader) (*http.ServeMux, string) {
	t.Helper()
	secret := "a-long-random-admin-secret"
	now := time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC)
	mux := http.NewServeMux()
	if !Register(mux, Deps{
		Store:         store,
		TokenSHA256:   digest(secret),
		Version:       "v1.2.3-test",
		StartedAt:     now.Add(-26*time.Hour - 4*time.Minute),
		Now:           func() time.Time { return now },
		AccessMetrics: access,
		Activity:      activityReader,
	}) {
		t.Fatal("valid token hash did not register /admin")
	}
	return mux, secret
}

func TestExternalNetworkEstimatesAreKoreanHonestAndOwnerExcluded(t *testing.T) {
	reader := &fakeActivityReader{metrics: activity.Metrics{
		Counts:    activity.Counts{ExternalDAU: 3, ExternalMAU: 9, OwnerDAU: 1, OwnerMAU: 2, DaySeen: true, MonthSeen: true},
		Daily:     activity.BuildDailyWindow(time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC), activity.DailyRaw{OldestEpoch: "2026-08-15", Days: []activity.DayCount{{Epoch: "2026-08-15", Count: 2, Rows: 2, Healthy: true}, {Epoch: "2026-08-16", Healthy: true}, {Epoch: "2026-08-17", Count: 3, Rows: 3, Healthy: true}}}),
		Telemetry: activity.Telemetry{Dropped: 2, StoreFailures: 1, Flushes: 7, Pending: 4},
	}}
	mux, secret := configuredMuxFull(t, &fakeStore{}, nil, reader)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.RemoteAddr = "198.51.100.77:4567"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	req.Header.Set("User-Agent", "private-owner-agent")
	req.SetBasicAuth("admin", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		// The two headline labels are exact and load-bearing: they say ID, not
		// user, and they name the period they actually cover.
		"오늘 API 활동 ID", "이번 달 API 활동 ID",
		">3<", ">9<",
		// The copy must rule out the three readings an operator would
		// otherwise reach for on its own.
		"사람 수가 아니고", "MCP 클라이언트나 세션 수도", "달력 기준 월(UTC 1일부터 말일까지)", "최근 30일이 아니라",
		"네트워크 추정치", "소유자 네트워크 제외", "사람/사용자 수가 아님", "이 활동 추정 수집기", "활동 추정 저장소",
		"소유자 제외 1개", "소유자 제외 2개", "공유 NAT·통신사망·회사망", "되돌릴 수 없는 버킷 승격",
		"CSX_ACTIVITY_HASH_KEY가 개인정보 보호 경계", "IPv4 버킷은 2^32 주소 공간 열거",
		"키가 없거나 잘못되어 수집이 꺼진 동안에도", "현재 UTC 기간을 포함해 일 버킷 35개·월 버킷 13개", "서버의 다른 기능",
		"대기열 포화 누락 2건", "저장 실패 1건", "성공 플러시 7회",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Korean estimate view missing %q", want)
		}
	}
	for _, pii := range []string{"198.51.100.77", "203.0.113.99", "private-owner-agent"} {
		if strings.Contains(body, pii) {
			t.Errorf("dashboard returned request PII %q", pii)
		}
	}
	if strings.Contains(body, "서버는 IP") || strings.Contains(body, "서버에 IP") {
		t.Fatal("activity privacy copy makes an unsupported server-wide no-IP claim")
	}
	if reader.markCalls != 1 || reader.metricCalls != 1 {
		t.Fatalf("activity calls = mark:%d metrics:%d, want 1 each", reader.markCalls, reader.metricCalls)
	}
}

// The daily chart has to keep four claims apart: counted traffic, a
// health-proven zero, a collection gap, and a day before collection.
func TestDailyActivityChartSeparatesCountsHealthyZerosGapsAndPreCollectionDays(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC)
	window := activity.BuildDailyWindow(now, activity.DailyRaw{
		OldestEpoch: "2026-08-13",
		Days: []activity.DayCount{
			{Epoch: "2026-08-13", Count: 5, Rows: 5, Healthy: true},
			{Epoch: "2026-08-15", Healthy: true},
			// Observed, but every bucket was the owner's: counted evidence, not a gap.
			{Epoch: "2026-08-16", Count: 0, OwnerExcluded: 2, Rows: 2, Healthy: true},
			{Epoch: "2026-08-17", Count: 3, Rows: 3, Healthy: true},
		},
	})
	plot := buildActivityDaily(window)
	if len(plot.Pending) != activity.DailyWindowDays-5 {
		t.Fatalf("pre-collection columns = %d, want %d", len(plot.Pending), activity.DailyWindowDays-5)
	}
	if len(plot.Gaps) != 1 || plot.Gaps[0].Day != "2026-08-14" {
		t.Fatalf("gap columns = %+v, want only 2026-08-14", plot.Gaps)
	}
	if len(plot.HealthyZeros) != 1 || plot.HealthyZeros[0].Day != "2026-08-15" {
		t.Fatalf("healthy-zero columns = %+v, want only 2026-08-15", plot.HealthyZeros)
	}
	if len(plot.Bars) != 3 {
		t.Fatalf("counted columns = %d, want 3 (including the owner-only zero day)", len(plot.Bars))
	}
	if plot.Bars[1].Day != "2026-08-16" || plot.Bars[1].Value != 0 {
		t.Fatalf("owner-only day = %+v, want a collected zero rather than a gap", plot.Bars[1])
	}
	if plot.Max != 5 || plot.DayFrom != "2026-07-18" || plot.DayTo != "2026-08-17" {
		t.Fatalf("plot range = %s~%s max=%d", plot.DayFrom, plot.DayTo, plot.Max)
	}
	if plot.StartEpoch != "2026-08-13" || plot.GapDays != 1 || !plot.Collecting || plot.Empty {
		t.Fatalf("plot summary = %+v", plot)
	}
	if !strings.Contains(plot.RangeNote, "수집 시작 2026-08-13") || !strings.Contains(plot.RangeNote, "수집 공백 1일") {
		t.Fatalf("range note does not report collection start and gaps: %q", plot.RangeNote)
	}

	// Nothing retained at all must never render 31 zero bars.
	empty := buildActivityDaily(activity.BuildDailyWindow(now, activity.DailyRaw{}))
	if len(empty.Bars) != 0 || len(empty.Gaps) != 0 || len(empty.Pending) != activity.DailyWindowDays || !empty.Empty || empty.Collecting {
		t.Fatalf("empty plot = %+v, want every column marked pre-collection", empty)
	}
}

func TestExternalNetworkEstimateNoRequestAndOwnerMarkFailureStates(t *testing.T) {
	t.Run("no request", func(t *testing.T) {
		reader := &fakeActivityReader{}
		mux, secret := configuredMuxFull(t, &fakeStore{}, nil, reader)
		body := serve(mux, http.MethodGet, "/admin", "admin", secret).Body.String()
		if !strings.Contains(body, "아직 의미 있는 API 요청 없음") {
			t.Fatalf("no-request state missing: %s", body)
		}
	})
	t.Run("owner mark error", func(t *testing.T) {
		reader := &fakeActivityReader{markErr: errors.New("db down")}
		mux, secret := configuredMuxFull(t, &fakeStore{}, nil, reader)
		body := serve(mux, http.MethodGet, "/admin", "admin", secret).Body.String()
		if !strings.Contains(body, "소유자 네트워크 제외를 확인할 수 없습니다") {
			t.Fatalf("owner error missing: %s", body)
		}
		if reader.metricCalls != 0 {
			t.Fatal("dashboard claimed metrics after owner exclusion failed")
		}
	})
	t.Run("invalid dedicated key", func(t *testing.T) {
		reader := &fakeActivityReader{markErr: activity.ErrInvalidKey}
		mux, secret := configuredMuxFull(t, &fakeStore{}, nil, reader)
		body := serve(mux, http.MethodGet, "/admin", "admin", secret).Body.String()
		if !strings.Contains(body, "활동 해시 키 구성이 올바르지 않아") {
			t.Fatalf("invalid-key state missing: %s", body)
		}
	})
}

func serve(mux *http.ServeMux, method, target, username, secret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if username != "" || secret != "" {
		req.SetBasicAuth(username, secret)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestMissingOrMalformedDigestLeavesRouteAbsent(t *testing.T) {
	bad := []string{
		"",
		"raw-admin-token",
		strings.Repeat("0", 63),
		strings.Repeat("0", 65),
		strings.Repeat("z", 64),
		" " + strings.Repeat("0", 64),
	}
	for _, tokenHash := range bad {
		t.Run(tokenHash, func(t *testing.T) {
			mux := http.NewServeMux()
			if Register(mux, Deps{TokenSHA256: tokenHash}) {
				t.Fatal("malformed digest registered the route")
			}
			rec := serve(mux, http.MethodGet, "/admin", "", "")
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestAuthUsesFixedHashSemanticsForEveryCredentialShape(t *testing.T) {
	store := &fakeStore{}
	mux, secret := configuredMux(t, store)

	tests := []struct {
		name     string
		username string
		password string
		want     int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "wrong password", username: "admin", password: "wrong", want: http.StatusUnauthorized},
		{name: "wrong username", username: "operator", password: secret, want: http.StatusUnauthorized},
		{name: "correct", username: "admin", password: secret, want: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(mux, http.MethodGet, "/admin", tc.username, tc.password)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}

	// A query parameter is never an alternate authentication channel.
	rec := serve(mux, http.MethodGet, "/admin?token="+secret, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("query token status = %d, want 401", rec.Code)
	}
	if store.statsCalls != 1 {
		t.Fatalf("store probes = %d, want only the one authorized request", store.statsCalls)
	}
}

func TestPrivateHeadersApplyToSuccessUnauthorizedAndRejectedMethod(t *testing.T) {
	mux, secret := configuredMux(t, &fakeStore{})
	cases := []struct {
		method string
		user   string
		pass   string
		want   int
	}{
		{http.MethodGet, "admin", secret, http.StatusOK},
		{http.MethodGet, "", "", http.StatusUnauthorized},
		{http.MethodPost, "admin", secret, http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		rec := serve(mux, tc.method, "/admin", tc.user, tc.pass)
		if rec.Code != tc.want {
			t.Fatalf("%s status = %d, want %d", tc.method, rec.Code, tc.want)
		}
		for name, want := range map[string]string{
			"Cache-Control":                "private, no-store, max-age=0",
			"X-Robots-Tag":                 "noindex, nofollow, noarchive",
			"Referrer-Policy":              "no-referrer",
			"X-Frame-Options":              "DENY",
			"X-Content-Type-Options":       "nosniff",
			"Cross-Origin-Resource-Policy": "same-origin",
		} {
			if got := rec.Header().Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		csp := rec.Header().Get("Content-Security-Policy")
		for _, directive := range []string{"default-src 'none'", "form-action 'none'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, directive) {
				t.Errorf("CSP %q missing %q", csp, directive)
			}
		}
	}
}

func TestDashboardShowsOnlyHonestBoundedMetrics(t *testing.T) {
	daily := make([]serverstore.AdminDailyStat, 0, 5)
	for i, samples := range []int64{900, 910, 925, 940, 951} {
		day := time.Date(2026, 8, 12+i, 0, 0, 0, 0, time.UTC)
		daily = append(daily, serverstore.AdminDailyStat{
			Day:             day,
			Evidence:        serverstore.AdminMetricValue{Value: 44000 + int64(i)*300, Valid: true},
			VerifiedSamples: serverstore.AdminMetricValue{Value: samples, Valid: true},
			Packages:        serverstore.AdminMetricValue{Value: 1200 + int64(i)*8, Valid: true},
		})
	}
	store := &fakeStore{
		counts: serverstore.NetworkCounts{
			Peers: 7, ProjectsMonth: 11, Packages: 1234, Observations: 45213,
			VerifiedSamples: 951, ServingPeers: 2,
		},
		wanted: []serverstore.WantedRow{{
			Ecosystem: "npm", Name: "three", Version: "0.180.0", Symbol: "Scene",
			Asks: 8, LastSeen: time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC),
		}},
		wantedTotal:       31,
		adoption:          serverstore.AdoptionCounts{Reports: 20, Applied: 14, BuildPass: 10, BuildFail: 2},
		insightsAvailable: true,
		insights: serverstore.AdminInsights{
			Daily:        daily,
			Verification: serverstore.AdminVerificationCounts{Pass: 72, Fail: 5, Skipped: 3},
			Ecosystems:   []serverstore.AdminEcosystemCount{{Ecosystem: "npm", Verifications: 70}, {Ecosystem: "pypi", Verifications: 10}},
			PackageDepth: []serverstore.AdminPackageDepth{{Ecosystem: "npm", Name: "three", VerifiedSamples: 16}},
		},
	}
	access := &fakeAccessReader{metrics: AccessLogMetrics{
		Days: []AccessLogDay{
			{Date: "2026-08-16", HasRequests: true, AccessStatusCounts: AccessStatusCounts{Requests: 34071}, Groups: []AccessGroupCounts{{Label: "검색·전달", AccessStatusCounts: AccessStatusCounts{Requests: 25000}}, {Label: "피드백·기여", AccessStatusCounts: AccessStatusCounts{Requests: 6000}}, {Label: "조정·메타데이터", AccessStatusCounts: AccessStatusCounts{Requests: 3071}}}},
			{Date: "2026-08-17", HasRequests: true, AccessStatusCounts: AccessStatusCounts{Requests: 35396}, Groups: []AccessGroupCounts{{Label: "검색·전달", AccessStatusCounts: AccessStatusCounts{Requests: 26000}}, {Label: "피드백·기여", AccessStatusCounts: AccessStatusCounts{Requests: 6000}}, {Label: "조정·메타데이터", AccessStatusCounts: AccessStatusCounts{Requests: 3396}}}},
		},
		Routes:              []AccessRouteCounts{{Label: "검색", GroupLabel: "POST 기여 · 기타 조정", AccessStatusCounts: AccessStatusCounts{Requests: 40000, GetHeadRequests: 8000, PostRequests: 31990, OtherMethodRequests: 10, Status429: 7, Status5xx: 2}}},
		Groups:              []AccessGroupCounts{{Label: "검색·전달", AccessStatusCounts: AccessStatusCounts{Requests: 51000}}, {Label: "피드백·기여", AccessStatusCounts: AccessStatusCounts{Requests: 12000}}, {Label: "조정·메타데이터", AccessStatusCounts: AccessStatusCounts{Requests: 6467}}},
		Totals:              AccessStatusCounts{Requests: 69467, GetHeadRequests: 60000, PostRequests: 9467, Status2xx: 68000, Status4xx: 1200, Status429: 42, Status5xx: 267},
		CollectionStartedAt: time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC),
		OldestEventAt:       time.Date(2026, 8, 16, 1, 0, 0, 0, time.UTC),
		NewestEventAt:       time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		DaysWithRequests:    2, SourceFiles: 2,
	}}
	mux, secret := configuredMuxWithAccess(t, store, access)
	rec := serve(mux, http.MethodGet, "/admin", "admin", secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<html lang="ko">`, "CodeSampleX 운영 대시보드", "30일 성장 추이", "누적", "일일 순증감",
		"1,234", "45,213", "951", "미응답 좌표 31개", "npm/three", "0.180.0", "Scene",
		"누락 날짜는 0으로 채우거나 선으로 연결하지 않음", "전체 네트워크에 접수된 검증 영수증",
		"최근 검증 생태계 구성", "npm · JavaScript/TypeScript", "최근 패키지 깊이", "원시 API 요청 횟수가 아닙니다",
		"API 요청 활동", "사용자 수가 아니라", "69,467", "35,396", "일별 전체 API 요청", "많이 호출된 API 종류", "<th scope=\"col\">기타</th>", "POST 기여 · 기타 조정",
		"API 종류별 요청 방식 및 응답 상태 집계", "최근 30일 패키지별 검증 샘플 수", "미응답 요청 패키지 좌표", "격리된 샘플은 제외합니다",
		"‘실패 회피’는 추정하지 않습니다", "해석할 때 제외해야 할 것", "활성 MCP 세션", "설치 및 다운로드",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing honest label %q", want)
		}
	}
	for _, forbidden := range []string{"<form", "<button", "ZgotmplZ", secret} {
		if strings.Contains(body, forbidden) {
			t.Errorf("body unexpectedly contains %q", forbidden)
		}
	}
	if got := strings.Count(body, `class="table-wrap"`); got != 3 {
		t.Errorf("mobile table wrappers = %d, want 3", got)
	}
	if got := strings.Count(body, `<caption class="sr-only">`); got != 3 {
		t.Errorf("accessible table captions = %d, want 3", got)
	}
	if got := strings.Count(body, `scope="col"`); got != 15 {
		t.Errorf("scoped table headers = %d, want 15", got)
	}
	if store.wantedQuery != "" || store.wantedOffset != 0 || store.wantedLimit != topWantedLimit {
		t.Errorf("Wanted query = (%q,%d,%d), want bounded top page (\"\",0,%d)",
			store.wantedQuery, store.wantedOffset, store.wantedLimit, topWantedLimit)
	}
	if store.statsCalls != 1 || store.countsCalls != 1 || store.wantedCalls != 1 || store.adoptionCalls != 1 || store.insightsCalls != 1 {
		t.Errorf("query calls = stats:%d counts:%d wanted:%d adoption:%d insights:%d, want one each",
			store.statsCalls, store.countsCalls, store.wantedCalls, store.adoptionCalls, store.insightsCalls)
	}
	if access.calls != 1 {
		t.Errorf("access metric calls = %d, want 1", access.calls)
	}
}

func TestDashboardDoesNotInventZeroTargetWhenVerifiedCountIsUnavailable(t *testing.T) {
	store := &fakeStore{
		countsErr:         errors.New("counts unavailable"),
		insightsAvailable: true,
		insights: serverstore.AdminInsights{Daily: []serverstore.AdminDailyStat{{
			Day:      time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
			Evidence: serverstore.AdminMetricValue{Value: 42, Valid: true},
			// VerifiedSamples is deliberately missing, not a measured zero.
		}}},
	}
	mux, secret := configuredMux(t, store)
	rec := serve(mux, http.MethodGet, "/admin", "admin", secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"검증된 샘플 10K 목표", "집계 없음", "현재 검증 샘플 집계를 읽지 못했습니다."} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing unavailable-target label %q", want)
		}
	}
	for _, falseClaim := range []string{"0 / 10,000", "남은 샘플 0개", `aria-label="10K 목표 진행률"`} {
		if strings.Contains(body, falseClaim) {
			t.Errorf("body invented unavailable target value %q", falseClaim)
		}
	}
}

func TestHeadHasHeadersAndNoBody(t *testing.T) {
	mux, secret := configuredMux(t, &fakeStore{})
	rec := serve(mux, http.MethodHead, "/admin", "admin", secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body length = %d, want 0", rec.Body.Len())
	}
	if rec.Header().Get("Content-Length") == "" {
		t.Fatal("HEAD response omitted representation Content-Length")
	}
}

func TestReadProbeFailureIsShownWithoutInventingHealth(t *testing.T) {
	store := &fakeStore{statsErr: errors.New("database is down")}
	mux, secret := configuredMux(t, store)
	rec := serve(mux, http.MethodGet, "/admin", "admin", secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 dashboard with partial state", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "사용 불가") || strings.Contains(body, "정상 ·") {
		t.Fatalf("database status was not honest: %s", body)
	}
}
