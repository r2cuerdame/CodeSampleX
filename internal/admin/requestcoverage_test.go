package admin

import (
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func requestCoverageFixture() serverstore.AdminRequestCoverage {
	return serverstore.AdminRequestCoverage{
		WindowStart: "2026-08-18", WindowEnd: "2026-08-24", TotalImpact: 27,
		Nodes: []serverstore.AdminCoverageNode{
			{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post", Impact: 5, PackageImpact: 10, PackageCoordinates: 3, LastDay: "2026-08-24", State: serverstore.AdminCoverageMiss, Boundary: "no_evidence", InMap: true, InTopMissing: true},
			{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.get", TargetOS: "windows", Impact: 3, PackageImpact: 10, PackageCoordinates: 3, LastDay: "2026-08-23", State: serverstore.AdminCoveragePartial, Boundary: "environment_gap", NearestEnvironment: "linux", FailureStage: "PROJECT_TEST", FailureFingerprint: "sha256:0123456789abcdef0123456789", InMap: true, InTopMissing: true},
			{Ecosystem: "npm", Name: "axios", Version: "1.11.0", Impact: 2, PackageImpact: 10, PackageCoordinates: 3, LastDay: "2026-08-22", State: serverstore.AdminCoverageHit, Boundary: "exact_pass", InMap: true},
			{Ecosystem: "pypi", Name: "requests", Version: "2.32.3", Symbol: "Session.get", Impact: 4, PackageImpact: 4, PackageCoordinates: 1, LastDay: "2026-08-24", State: serverstore.AdminCoveragePartial, Boundary: "symbol_gap", InMap: true, InTopMissing: true},
			// This coordinate is deliberately outside the map candidate set. The
			// global Top Missing ranking must still include it ahead of every map row.
			{Ecosystem: "cargo", Name: "tokio", Version: "1.0.0", Symbol: "spawn", Impact: 9, PackageImpact: 9, PackageCoordinates: 1, LastDay: "2026-08-24", State: serverstore.AdminCoverageMiss, Boundary: "no_evidence", InTopMissing: true},
		},
	}
}

func TestRequestCoverageRanksByRecentImpactAndKeepsMeasurementBoundary(t *testing.T) {
	raw := requestCoverageFixture()
	view := buildRequestCoverageView(raw, serverstore.AdminFlowWindow{Hits: 12, NoMatches: 8})
	if !view.Available || view.TotalImpact != 27 || view.MappedImpact != 14 || view.MissingImpact != 21 {
		t.Fatalf("coverage totals = %+v", view)
	}
	if !view.SearchAvailable || view.Searches != 20 || view.HitRate != "60.0%" {
		t.Fatalf("global search basis = %+v", view)
	}
	if len(view.Packages) != 2 || view.Packages[0].Key != "npm/axios" || view.Packages[0].Impact != 10 {
		t.Fatalf("packages = %+v", view.Packages)
	}
	if len(view.Missing) != 4 || view.Missing[0].PackageKey != "cargo/tokio" {
		t.Fatalf("missing rows = %+v", view.Missing)
	}
	for i, want := range []int64{9, 5, 4, 3} {
		if view.Missing[i].Impact != want {
			t.Fatalf("missing rank %d impact = %d, want %d", i, view.Missing[i].Impact, want)
		}
	}
	if got := view.Missing[3]; got.BoundaryLabel != "다른 환경(linux)에만 PASS" || got.FailureLabel != "PROJECT_TEST · sha256:0123456789a…" {
		t.Fatalf("nearest evidence boundary = %+v", got)
	}
	for _, row := range view.Missing {
		if strings.Contains(row.Key, "anon") {
			t.Fatalf("stable reporter leaked into view: %+v", row)
		}
	}
}

func TestRequestCoverageTreemapAreaIsProportionalToRecentImpact(t *testing.T) {
	view := buildRequestCoverageView(requestCoverageFixture(), serverstore.AdminFlowWindow{})
	if len(view.Packages) != 2 {
		t.Fatalf("packages = %+v", view.Packages)
	}
	packageAreaRatio := view.Packages[0].Width / view.Packages[1].Width
	if math.Abs(packageAreaRatio-2.5) > 0.000001 {
		t.Fatalf("package area ratio = %f, want 10/4", packageAreaRatio)
	}
	var five, two coverageRowView
	for _, version := range view.Packages[0].Versions {
		for _, node := range version.Nodes {
			switch node.Impact {
			case 5:
				five = node
			case 2:
				two = node
			}
		}
	}
	leafAreaRatio := (five.Width * five.Height) / (two.Width * two.Height)
	if math.Abs(leafAreaRatio-2.5) > 0.000001 {
		t.Fatalf("leaf area ratio = %f, want 5/2", leafAreaRatio)
	}
}

func TestDashboardRendersRequestCoverageMapRankingAndEmptyState(t *testing.T) {
	store := &fakeStore{insightsAvailable: true, insights: serverstore.AdminInsights{
		Coverage: requestCoverageFixture(),
		Flow:     serverstore.AdminFlow{Week: serverstore.AdminFlowWindow{Length: 7 * 24 * time.Hour, Hits: 12, NoMatches: 8}},
	}}
	mux, secret := configuredMux(t, store)
	body := serve(mux, http.MethodGet, "/admin", "recuerdame", secret).Body.String()
	for _, want := range []string{
		"Request Coverage Map", "Top Missing Areas", "npm/axios", "axios.post", "cargo/tokio",
		"부분/약한 커버리지", "미커버 MISS", "좌표 hit rate", "측정 불가",
		"다른 환경(linux)에만 PASS", "Coverage Gap Graph", "request-coverage.js",
		"원문 query, IP, User-Agent, 안정 사용자 ID는 저장하거나 표시하지 않습니다",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("request coverage rendering missing %q", want)
		}
	}
	for _, forbidden := range []string{"anon_id", "raw IP", "raw UA"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("request coverage rendering leaked %q", forbidden)
		}
	}

	empty := &fakeStore{insightsAvailable: true, insights: serverstore.AdminInsights{
		Coverage: serverstore.AdminRequestCoverage{WindowStart: "2026-08-18", WindowEnd: "2026-08-24"},
	}}
	emptyMux, emptySecret := configuredMux(t, empty)
	emptyBody := serve(emptyMux, http.MethodGet, "/admin", "recuerdame", emptySecret).Body.String()
	if !strings.Contains(emptyBody, "이 창에 지도화할 좌표가 없다는 뜻입니다") {
		t.Fatal("empty request coverage state was not rendered honestly")
	}
}

func TestRequestCoverageScriptIsPrivateAndAuthenticated(t *testing.T) {
	mux, secret := configuredMux(t, &fakeStore{})
	unauthorized := serve(mux, http.MethodGet, "/admin/request-coverage.js", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized script status = %d", unauthorized.Code)
	}
	authorized := serve(mux, http.MethodGet, "/admin/request-coverage.js", "recuerdame", secret)
	if authorized.Code != http.StatusOK || !strings.Contains(authorized.Body.String(), "coverage-missing-row") {
		t.Fatalf("authorized script = %d %q", authorized.Code, authorized.Body.String())
	}
	if !strings.Contains(authorized.Body.String(), `querySelectorAll("button.coverage-node")`) {
		t.Fatal("script must exclude the non-interactive omitted-impact rectangle")
	}
	if got := authorized.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("script cache policy = %q", got)
	}
}
