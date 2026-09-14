package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const anonymousTestPassword = "local-anonymous-test-secret"

func anonymousTestMux(t *testing.T, store serverstore.AnonymousAnalyticsStore) *http.ServeMux {
	t.Helper()
	hash := sha256.Sum256([]byte(anonymousTestPassword))
	mux := http.NewServeMux()
	if !Register(mux, Deps{Store: &fakeStore{}, Anonymous: store, TokenSHA256: hex.EncodeToString(hash[:]), Now: func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }}) {
		t.Fatal("registration")
	}
	return mux
}
func anonymousFixture(t *testing.T) *serverstore.Fake {
	t.Helper()
	f := serverstore.NewFake()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for i := range 40 {
		for day := i % 12; day < 70; day += 1 + i%7 {
			if err := f.RecordAnonymousClient(context.Background(), fmt.Sprintf("%064x", i+1), now.AddDate(0, 0, -day)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}
func TestAnonymousAdminAuthAndCharts(t *testing.T) {
	mux := anonymousTestMux(t, anonymousFixture(t))
	r := httptest.NewRequest("GET", "/admin", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("analytics exposed without auth")
	}
	r.SetBasicAuth("recuerdame", anonymousTestPassword)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for _, s := range []string{"NRU", "DAU", "MAU", "익명 활동 · 성공 API 요청", "코호트 유지율", "D30", "시계열 데이터 표", "익명 무료 사용자 분석"} {
		if !strings.Contains(w.Body.String(), s) {
			t.Fatalf("missing %s", s)
		}
	}
	if strings.Contains(w.Body.String(), fmt.Sprintf("%064x", 1)) {
		t.Fatal("client hash exposed")
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("admin cache enabled")
	}
}

type anonymousReadError struct{ *serverstore.Fake }

func (anonymousReadError) AnonymousAnalytics(context.Context, time.Time) (serverstore.AnonymousAnalytics, error) {
	return serverstore.AnonymousAnalytics{}, errors.New("private diagnostic")
}
func TestAnonymousAdminMissingAndFailedAreNotZero(t *testing.T) {
	for _, store := range []serverstore.AnonymousAnalyticsStore{nil, anonymousReadError{serverstore.NewFake()}} {
		mux := anonymousTestMux(t, store)
		r := httptest.NewRequest("GET", "/admin", nil)
		r.SetBasicAuth("recuerdame", anonymousTestPassword)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		if strings.Contains(w.Body.String(), "private diagnostic") {
			t.Fatal("error detail leaked")
		}
		if strings.Contains(w.Body.String(), "NRU · 오늘 신규") {
			t.Fatal("unavailable rendered as zero")
		}
	}
}
func TestAnonymousRetentionUsesEligibleWeightedDenominators(t *testing.T) {
	m := serverstore.AnonymousAnalytics{Cohorts: []serverstore.AnonymousCohort{
		{Size: 2, Retention: []serverstore.AnonymousRetention{{Day: 1, Active: 1, Eligible: true}}},
		{Size: 8, Retention: []serverstore.AnonymousRetention{{Day: 1, Active: 0, Eligible: true}}},
		{Size: 100, Retention: []serverstore.AnonymousRetention{{Day: 1, Eligible: false}}},
	}}
	v := buildAnonymousView(m)
	if v.Retention[0].Rate != "10.0%" || v.Retention[0].Size != 10 {
		t.Fatal(v.Retention)
	}
}

// A local fixture for Playwright, isolated from production and other fixtures.
func TestServeAnonymousBrowserFixture(t *testing.T) {
	if os.Getenv("CSX_ANONYMOUS_BROWSER") != "1" {
		t.Skip("set CSX_ANONYMOUS_BROWSER=1 for local browser fixture")
	}
	mux := anonymousTestMux(t, anonymousFixture(t))
	s := &http.Server{Addr: "127.0.0.1:18987", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { s.Close() })
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
