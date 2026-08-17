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

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type fakeStore struct {
	statsErr    error
	counts      serverstore.NetworkCounts
	countsErr   error
	wanted      []serverstore.WantedRow
	wantedTotal int
	wantedErr   error
	adoption    serverstore.AdoptionCounts
	adoptionErr error

	statsCalls    int
	countsCalls   int
	wantedCalls   int
	adoptionCalls int
	wantedQuery   string
	wantedOffset  int
	wantedLimit   int
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

func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func configuredMux(t *testing.T, store Store) (*http.ServeMux, string) {
	t.Helper()
	secret := "a-long-random-admin-secret"
	now := time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC)
	mux := http.NewServeMux()
	if !Register(mux, Deps{
		Store:       store,
		TokenSHA256: digest(secret),
		Version:     "v1.2.3-test",
		StartedAt:   now.Add(-26*time.Hour - 4*time.Minute),
		Now:         func() time.Time { return now },
	}) {
		t.Fatal("valid token hash did not register /admin")
	}
	return mux, secret
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
	store := &fakeStore{
		counts: serverstore.NetworkCounts{
			Peers: 7, ProjectsMonth: 11, Packages: 1234, Observations: 45213,
			VerifiedSamples: 951, ServingPeers: 2,
		},
		wanted: []serverstore.WantedRow{{
			Ecosystem: "npm", Name: "three", Version: "0.180.0", Symbol: "Scene",
			Asks: 8, LastSeen: time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC),
		}},
		wantedTotal: 31,
		adoption:    serverstore.AdoptionCounts{Reports: 20, Applied: 14, BuildPass: 10, BuildFail: 2},
	}
	mux, secret := configuredMux(t, store)
	rec := serve(mux, http.MethodGet, "/admin", "admin", secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<html lang="ko">`, "CodeSampleX 운영 현황", "1,234", "45,213", "951", "미응답 좌표 31개", "npm/three", "0.180.0", "Scene",
		"매일 바뀌는 익명 버킷입니다. 누적 사용자나 활성 MCP 세션 수가 아닙니다.",
		"매월 바뀌는 익명 버킷입니다. 계정·설치·고유 사용자 수가 아닙니다.",
		"원시 요청 횟수가 아닙니다", "‘실패 회피’는 추정하지 않습니다", "아직 측정하지 않는 항목",
		"활성 MCP 세션", "설치 및 다운로드", "30일 추세",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing honest label %q", want)
		}
	}
	for _, forbidden := range []string{"<form", "<button", secret} {
		if strings.Contains(body, forbidden) {
			t.Errorf("body unexpectedly contains %q", forbidden)
		}
	}
	if store.wantedQuery != "" || store.wantedOffset != 0 || store.wantedLimit != topWantedLimit {
		t.Errorf("Wanted query = (%q,%d,%d), want bounded top page (\"\",0,%d)",
			store.wantedQuery, store.wantedOffset, store.wantedLimit, topWantedLimit)
	}
	if store.statsCalls != 1 || store.countsCalls != 1 || store.wantedCalls != 1 || store.adoptionCalls != 1 {
		t.Errorf("query calls = stats:%d counts:%d wanted:%d adoption:%d, want one each",
			store.statsCalls, store.countsCalls, store.wantedCalls, store.adoptionCalls)
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
