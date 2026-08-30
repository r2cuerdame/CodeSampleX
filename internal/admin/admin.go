// Package admin serves the private operator dashboard. Network metrics stay
// read-only; the isolated sample-worker control plane issues and revokes
// narrowly scoped capabilities for refresh and private draft submission.
//
// It deliberately consumes only bounded aggregate reads. In particular it
// does not infer users, active MCP sessions, downloads, or factory activity
// from unrelated counters.
package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/activity"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const (
	dashboardTimeout = 5 * time.Second
	topWantedLimit   = 10
)

//go:embed templates/admin.html
var templateFS embed.FS

var dashboardTemplate = template.Must(template.New("admin.html").Funcs(template.FuncMap{
	"number":  formatInt,
	"numberu": formatUint,
}).ParseFS(templateFS, "templates/admin.html"))

// Store is the deliberately small read-only view needed by the dashboard.
// Every method returns a bounded aggregate or a bounded page.
type Store interface {
	GetLatestStats(ctx context.Context) (statsJSON string, ok bool, err error)
	NetworkCounts(ctx context.Context, now time.Time) (serverstore.NetworkCounts, error)
	ListWanted(ctx context.Context, query string, offset, limit int) (rows []serverstore.WantedRow, total int, err error)
	AdoptionSummary(ctx context.Context) (serverstore.AdoptionCounts, error)
	AdminInsights(ctx context.Context, now time.Time) (insights serverstore.AdminInsights, available bool, err error)
}

// AccessMetricsReader is the narrow read-only seam around privacy-safe,
// query-stripped HTTP access logs. It exposes request volume, never users.
type AccessMetricsReader interface {
	Metrics(ctx context.Context, now time.Time) (AccessLogMetrics, error)
}

// ActivityReader can mark the authenticated operator network and return only
// privacy-bounded, owner-excluded network estimates and collection telemetry.
type ActivityReader interface {
	MarkOwner(ctx context.Context, r *http.Request, now time.Time) error
	Metrics(ctx context.Context, now time.Time) (activity.Metrics, error)
	Telemetry() activity.Telemetry
}

// Deps wires the private dashboard. TokenSHA256 must be the 64-character
// hexadecimal SHA-256 digest of the HTTP Basic password; a raw token is never
// accepted. The username is always "recuerdame".
type Deps struct {
	Store         Store
	TokenSHA256   string
	PublicURL     string
	Version       string
	StartedAt     time.Time
	Now           func() time.Time
	AccessMetrics AccessMetricsReader
	Activity      ActivityReader
	Authoring     serverstore.AuthoringSessionStore
	// AdminTokens backs the operator API credential. Nil leaves the admin
	// surface reachable only through the browser's Basic prompt.
	AdminTokens serverstore.AdminTokenStore
	// Farm backs the operations panel. Nil hides it rather than showing zeros.
	Farm serverstore.FarmStatsStore
	// Anomalies backs the consumption-feedback panel. Nil hides it: a store
	// that cannot answer has nothing honest to show, and a panel of zeros
	// reads as "nobody reported anything" rather than "not measured here".
	Anomalies serverstore.AnomalyStore
	// CSXIssues backs the product-defect panel. Nil hides it, for the same
	// reason: a panel of zeros reads as "nobody reported anything" rather
	// than "not measured here".
	CSXIssues serverstore.CSXIssueStore
	// PoolStats backs the database pool panel. Nil hides it: a store
	// without a pool has nothing honest to report there.
	PoolStats PoolStatsReader
	// Instances are the machines being paid for, with their monthly price.
	Instances []Instance
}

type handler struct {
	store         Store
	wantHash      [sha256.Size]byte
	version       string
	startedAt     time.Time
	now           func() time.Time
	accessMetrics AccessMetricsReader
	activity      ActivityReader
	publicURL     string
	authoring     *authoringRegistry
	authoringRate *authoringRateLimiter
	adminTokens   serverstore.AdminTokenStore
	farmStats     serverstore.FarmStatsStore
	// farmGate admits one whole-corpus farm snapshot at a time. The browser
	// refreshes this panel on a timer; without a gate, a slow snapshot lets
	// every tick add another copy of the same PostgreSQL work.
	farmGate  chan struct{}
	anomalies serverstore.AnomalyStore
	csxIssues serverstore.CSXIssueStore
	poolStats PoolStatsReader
	instances []Instance
}

// Register mounts the exact /admin path only when TokenSHA256 is a valid
// digest. Returning false and registering nothing is intentional: absent or
// malformed configuration makes the private route indistinguishable from an
// unknown path (404), instead of accidentally falling back to another token.
func Register(mux *http.ServeMux, d Deps) bool {
	wantHash, ok := parseDigest(d.TokenSHA256)
	if !ok {
		return false
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	startedAt := d.StartedAt
	if startedAt.IsZero() {
		startedAt = now()
	}
	h := &handler{
		store:         d.Store,
		wantHash:      wantHash,
		version:       d.Version,
		startedAt:     startedAt,
		now:           now,
		accessMetrics: d.AccessMetrics,
		activity:      d.Activity,
		publicURL:     strings.TrimRight(d.PublicURL, "/"),
		authoring:     newAuthoringRegistry(now, d.Authoring),
		authoringRate: newAuthoringRateLimiter(),
		adminTokens:   d.AdminTokens,
		farmStats:     d.Farm,
		farmGate:      make(chan struct{}, 1),
		anomalies:     d.Anomalies,
		csxIssues:     d.CSXIssues,
		poolStats:     d.PoolStats,
		instances:     d.Instances,
	}
	// A methodless /admin pattern would conflict with the public website's
	// GET /{seg} route under Go's specificity rules. GET also covers HEAD;
	// explicit standard write methods reach the same read-only handler and
	// receive a header-hardened 405 without exposing any mutation surface.
	mux.Handle("GET /admin", h)
	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		http.MethodOptions, http.MethodConnect, http.MethodTrace,
	} {
		mux.Handle(method+" /admin", h)
	}
	mux.HandleFunc("GET /admin/admin.js", h.adminScript)
	mux.HandleFunc("HEAD /admin/admin.js", h.adminScript)
	mux.HandleFunc("GET /admin/api/authoring-sessions", h.authoringSessions)
	mux.HandleFunc("POST /admin/api/authoring-sessions", h.authoringSessions)
	mux.HandleFunc("DELETE /admin/api/authoring-sessions/{id}", h.revokeAuthoringSession)
	mux.HandleFunc("POST /admin/api/authoring-sessions/{id}/rotate", h.rotateAuthoringSession)
	mux.HandleFunc("GET /admin/api/farm", h.farm)
	mux.HandleFunc("POST /admin/api/csx-issues/verdict", h.setCSXIssueVerdict)
	mux.HandleFunc("POST /admin/api/csx-issues/canonical", h.linkCSXIssueCanonical)
	mux.HandleFunc("GET /admin/api/withheld-work", h.withheldWork)
	mux.HandleFunc("POST /admin/api/withheld-work/reopen", h.reopenWithheldWork)
	mux.HandleFunc("GET /admin/api/admin-tokens", h.handleAdminTokens)
	mux.HandleFunc("POST /admin/api/admin-tokens", h.handleAdminTokens)
	mux.HandleFunc("DELETE /admin/api/admin-tokens/{id}", h.revokeAdminToken)
	mux.HandleFunc("POST /v1/authoring/session/refresh", h.refreshAuthoringSession)
	return true
}

func parseDigest(raw string) ([sha256.Size]byte, bool) {
	var out [sha256.Size]byte
	// Do not trim: leading/trailing whitespace is malformed configuration,
	// not part of a permissive secret format.
	if len(raw) != hex.EncodedLen(len(out)) {
		return out, false
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != len(out) {
		return out, false
	}
	copy(out[:], decoded)
	return out, true
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "허용되지 않은 요청 방식입니다", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="CodeSampleX Admin", charset="UTF-8"`)
		http.Error(w, "인증이 필요합니다", http.StatusUnauthorized)
		return
	}

	now := h.now()
	data := dashboardData{
		Version:     h.version,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Uptime:      formatDuration(nonNegative(now.Sub(h.startedAt))),

		WorkerUnixSetupCMD: WorkerUnixCMD(workerInstallBase),
		WorkerUnixRunCMD:   WorkerUnixRunCMD(),
	}
	if data.Version == "" {
		data.Version = "알 수 없음"
	}

	ctx, cancel := context.WithTimeout(r.Context(), dashboardTimeout)
	defer cancel()
	if h.activity == nil {
		data.ActivityError = "외부 네트워크 추정 집계가 구성되지 않았습니다"
	} else {
		data.ActivityTelemetry = h.activity.Telemetry()
		if err := h.activity.MarkOwner(ctx, r, now); err != nil {
			// Without a confirmed owner mark, showing a number could count this
			// very admin request as external. Prefer explicit unavailability.
			data.ActivityTelemetry = h.activity.Telemetry()
			switch {
			case errors.Is(err, activity.ErrInvalidKey):
				data.ActivityError = "활동 해시 키 구성이 올바르지 않아 추정치를 제공하지 않습니다"
			case errors.Is(err, activity.ErrUnavailable):
				data.ActivityError = "API 활동 ID 집계가 구성되지 않았습니다"
			case errors.Is(err, activity.ErrNoNetworkIdentity):
				data.ActivityError = "이 요청의 네트워크 경계를 신뢰할 수 없어 소유자 네트워크 제외를 확인할 수 없습니다"
			default:
				data.ActivityError = "소유자 네트워크 제외를 확인할 수 없습니다"
			}
		} else if metrics, err := h.activity.Metrics(ctx, now); err != nil {
			data.ActivityTelemetry = metrics.Telemetry
			data.ActivityError = "API 활동 ID 추정치를 불러올 수 없습니다"
		} else {
			data.Activity = metrics
			data.ActivityDaily = buildActivityDaily(metrics.Daily)
			data.ActivityTelemetry = metrics.Telemetry
			data.ActivityAvailable = true
		}
	}
	if h.poolStats != nil {
		// Counters, not a query: the panel that explains a saturated pool
		// must not need the pool to render.
		data.DBPool = buildPoolView(h.poolStats.PoolStats())
	}
	if h.store == nil {
		data.DBError = "저장소가 구성되지 않았습니다"
	} else {
		h.collect(ctx, now, &data)
	}
	data.SourceIssues = dashboardSourceIssues(data)

	var body bytes.Buffer
	if err := dashboardTemplate.Execute(&body, data); err != nil {
		http.Error(w, "운영 화면을 렌더링하지 못했습니다", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body.Bytes())
}

func (h *handler) authorized(r *http.Request) bool {
	username, password, basicOK := r.BasicAuth()
	// Hash both candidates before deciding. The secret comparison is always
	// between fixed-size values and uses the constant-time primitive; there is
	// no plaintext or variable-length secret comparison on any auth path.
	passwordHash := sha256.Sum256([]byte(password))
	usernameHash := sha256.Sum256([]byte(username))
	wantUsernameHash := sha256.Sum256([]byte("recuerdame"))
	basic := 0
	if basicOK {
		basic = 1
	}
	match := subtle.ConstantTimeCompare(passwordHash[:], h.wantHash[:]) &
		subtle.ConstantTimeCompare(usernameHash[:], wantUsernameHash[:]) & basic
	return match == 1
}

func setPrivateHeaders(h http.Header) {
	h.Set("Cache-Control", "private, no-store, max-age=0")
	h.Set("Pragma", "no-cache")
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

func (h *handler) collect(ctx context.Context, now time.Time, data *dashboardData) {
	probeStart := h.now()
	_, _, err := h.store.GetLatestStats(ctx)
	data.DBProbe = formatLatency(nonNegative(h.now().Sub(probeStart)))
	if err != nil {
		data.DBError = "읽기 점검 실패"
	} else {
		data.DBHealthy = true
	}

	if counts, err := h.store.NetworkCounts(ctx, now); err != nil {
		data.CountsError = "네트워크 집계를 불러올 수 없습니다"
	} else {
		data.Counts = counts
		data.CountsAvailable = true
	}

	if rows, total, err := h.store.ListWanted(ctx, "", 0, topWantedLimit); err != nil {
		data.WantedError = "요청 요약을 불러올 수 없습니다"
	} else {
		data.Wanted = rows
		data.WantedTotal = total
		data.WantedAvailable = true
	}

	if adoption, err := h.store.AdoptionSummary(ctx); err != nil {
		data.AdoptionError = "채택 요약을 불러올 수 없습니다"
	} else {
		data.Adoption = adoption
		data.AdoptionAvailable = true
	}

	if insights, available, err := h.store.AdminInsights(ctx, now); err != nil {
		data.InsightsError = "30일 운영 추세를 불러올 수 없습니다"
	} else if !available {
		data.InsightsError = "현재 저장소는 30일 운영 추세를 제공하지 않습니다"
	} else {
		data.Insights = buildInsightView(insights, data.Counts, data.CountsAvailable, now)
		data.SearchQuality = buildSearchQualityView(insights.Search)
		data.Flow = buildFlowView(insights.Flow, insights.Jobs, now)
		data.InsightsAvailable = true
	}

	if h.anomalies == nil {
		data.AnomalyError = "이 저장소는 이상 신고 채널을 제공하지 않습니다"
	} else if insights, err := h.anomalies.AnomalyInsights(ctx, now, serverstore.AdminInsightDays); err != nil {
		data.AnomalyError = "이상 신고 집계를 불러올 수 없습니다"
	} else {
		recent, listErr := h.anomalies.ListAnomalyReports(ctx, anomalyRecentLimit)
		if listErr != nil {
			// The aggregate is still worth showing without the table; a
			// failed list is not a reason to hide the ratios that decide
			// whether anyone should look at this at all.
			recent = nil
		}
		data.Anomaly = buildAnomalyView(insights, recent, now)
		data.AnomalyAvailable = true
	}

	if h.csxIssues == nil {
		data.CSXIssueError = "이 저장소는 제품 결함 신고 채널을 제공하지 않습니다"
	} else if insights, err := h.csxIssues.CSXIssueInsights(ctx, now, serverstore.AdminInsightDays); err != nil {
		data.CSXIssueError = "제품 결함 신고 집계를 불러올 수 없습니다"
	} else {
		recent, listErr := h.csxIssues.ListCSXIssueReports(ctx, anomalyRecentLimit)
		if listErr != nil {
			recent = nil
		}
		data.CSXIssue = buildCSXIssueView(insights, recent, now)
		data.CSXIssueAvailable = true
	}

	if h.accessMetrics == nil {
		data.AccessError = "API 안전 로그 집계가 구성되지 않았습니다"
	} else if metrics, err := h.accessMetrics.Metrics(ctx, now); err != nil {
		data.AccessError = "API 요청 집계를 불러올 수 없습니다"
	} else {
		data.Access = buildAccessView(metrics, now)
		data.AccessAvailable = true
	}
}

type dashboardData struct {
	Version     string
	GeneratedAt string
	Uptime      string

	// Verification worker install commands. They used to be the call to
	// action on the public /contribute page, which is gone: a contributor
	// did exactly what any user does -- installing csx is what emits
	// observations -- except for running samples, and the project runs those
	// itself. What is left is an operator task, so it sits folded beside the
	// internal sample workers it pairs with.
	WorkerUnixSetupCMD string
	WorkerUnixRunCMD   string

	DBHealthy bool
	DBProbe   string
	DBError   string
	DBPool    poolView

	Counts          serverstore.NetworkCounts
	CountsAvailable bool
	CountsError     string

	Wanted          []serverstore.WantedRow
	WantedTotal     int
	WantedAvailable bool
	WantedError     string

	Adoption          serverstore.AdoptionCounts
	AdoptionAvailable bool
	AdoptionError     string

	Insights          insightView
	InsightsAvailable bool
	InsightsError     string

	Access          accessView
	AccessAvailable bool
	AccessError     string

	Activity          activity.Metrics
	ActivityDaily     activityDailyPlot
	ActivityTelemetry activity.Telemetry
	ActivityAvailable bool
	ActivityError     string

	SearchQuality searchQualityView

	// Anomaly is the consumption side answering back: what agents reported,
	// how much of it was the same thing twice, and how much of it turned out
	// to be real.
	Anomaly          anomalyView
	AnomalyAvailable bool
	AnomalyError     string

	// CSXIssue is the same channel aimed at this product rather than at a
	// package. Separate view, because they are separate things.
	CSXIssue          csxIssueView
	CSXIssueAvailable bool
	CSXIssueError     string

	// Flow is the production-rate half of the summary. Everything above it is
	// stock, and stock cannot answer whether the line is running right now.
	Flow flowView

	SourceIssues []string
}

func dashboardSourceIssues(data dashboardData) []string {
	issues := []string{
		data.DBError, data.CountsError, data.WantedError, data.AdoptionError,
		data.InsightsError,
	}
	if data.InsightsAvailable && !data.SearchQuality.Available {
		issues = append(issues, "공개 검색 결과 집계가 아직 없습니다")
	}
	seen := make(map[string]bool, len(issues))
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue == "" || seen[issue] {
			continue
		}
		seen[issue] = true
		out = append(out, issue)
	}
	return out
}

func nonNegative(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

func formatLatency(d time.Duration) string {
	if d < time.Millisecond {
		return "<1 ms"
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%d초", int64(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d분 %d초", int64(d/time.Minute), int64((d%time.Minute)/time.Second))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d시간 %d분", int64(d/time.Hour), int64((d%time.Hour)/time.Minute))
	}
	return fmt.Sprintf("%d일 %d시간", int64(d/(24*time.Hour)), int64((d%(24*time.Hour))/time.Hour))
}

func formatInt(n int64) string {
	negative := n < 0
	s := strconv.FormatInt(n, 10)
	if negative {
		s = strings.TrimPrefix(s, "-")
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if negative {
		return "-" + s
	}
	return s
}

func formatUint(n uint64) string {
	s := strconv.FormatUint(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
