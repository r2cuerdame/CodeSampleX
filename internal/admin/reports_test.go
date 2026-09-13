package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Opt-in real HTTP fixture for Playwright; never connects to production.
func TestServeReportBrowserFixture(t *testing.T) {
	addr := os.Getenv("CSX_ADMIN_BROWSER_ADDR")
	if addr == "" {
		t.Skip("set CSX_ADMIN_BROWSER_ADDR for the Playwright fixture")
	}
	if addr != "127.0.0.1:18986" {
		t.Fatal("fixture must bind loopback test port")
	}
	store := serverstore.NewFake()
	for i := 0; i < 31; i++ {
		_, _, err := store.RecordCSXIssueReport(context.Background(), serverstore.CSXIssueReportRow{Fingerprint: fmt.Sprintf("browser-%d", i), Component: fmt.Sprintf("component-%02d", i), Surface: "mcp", IssueKind: "runtime-behavior", Status: "no-replay-lane", ReplayReason: "no automatic replay", ReportJSON: `{"actualBehavior":"<script>alert(1)</script>","expectedBehavior":"safe evidence rendering","llmHypothesis":"unverified hypothesis"}`}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _, _ = store.RecordAnomalyReport(context.Background(), serverstore.AnomalyReportRow{Fingerprint: "browser-anomaly", PURL: "pkg:npm/example@1.0.0", Status: "unsupported", UnsupportedReason: "no sample supplied", ReportJSON: `{"localObserved":{"result":"FAIL"}}`}, time.Now())
	mux, secret := configuredMuxWithChannels(t, &fakeStore{}, store, store)
	// Match the fixture's origin while retaining production mutation checks.
	wrapper := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "http://"+addr {
			r.Header.Set("Origin", "https://codesamplex.dev")
		}
		mux.ServeHTTP(w, r)
	})
	_ = secret // fixed non-production test credential from configuredMuxWithChannels
	server := &http.Server{Addr: addr, Handler: wrapper, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { _ = server.Close() })
	t.Log("report browser fixture ready")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

func TestReportQueueAuthEvidenceAndReview(t *testing.T) {
	store := serverstore.NewFake()
	row, _, _ := store.RecordCSXIssueReport(context.Background(), serverstore.CSXIssueReportRow{Fingerprint: "private", ReportJSON: `{"actualBehavior":"<script>alert(1)</script>"}`, Component: "example", ReporterBucket: "private-bucket"}, time.Now())
	mux, secret := configuredMuxWithChannels(t, &fakeStore{}, store, store)
	get := func(path string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if auth {
			r.SetBasicAuth("recuerdame", secret)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/admin/api/reports", "/admin/api/reports/product/1", "/admin/reports.js"} {
		if w := get(path, false); w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
		if w := get(path, true); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("authenticated %s: %d", path, w.Code)
		}
	}
	queue := get("/admin/api/reports", true)
	if strings.Contains(queue.Body.String(), "alert") || strings.Contains(queue.Body.String(), "private-bucket") {
		t.Fatal("queue exposed submitted evidence or reporter")
	}
	detail := get("/admin/api/reports/product/1", true)
	if !strings.Contains(detail.Body.String(), "actualBehavior") || strings.Contains(detail.Body.String(), "private-bucket") || strings.Contains(detail.Body.String(), "<script>") {
		t.Fatal("detail did not preserve safely encoded evidence")
	}
	for _, path := range []string{"/admin/api/reports?offset=-1", "/admin/api/reports?channel=invalid", "/admin/api/reports?state=invalid", "/admin/api/reports/product/no"} {
		if w := get(path, true); w.Code != 400 {
			t.Fatalf("bad filter %s: %d", path, w.Code)
		}
	}
	if w := get("/admin/api/reports/product/999", true); w.Code != 404 {
		t.Fatal("missing report")
	}
	input := map[string]any{"id": row.ID, "verdict": "expected-behavior", "note": ""}
	if w := postAdminJSON(t, mux, "/admin/api/reports/review", input); w.Code != 400 {
		t.Fatal("review without rationale accepted")
	}
	input["note"] = strings.Repeat("measured evidence ", 170)
	raw, _ := json.Marshal(input)
	r := httptest.NewRequest("POST", "/admin/api/reports/review", strings.NewReader(string(raw)))
	r.SetBasicAuth("recuerdame", secret)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin review accepted")
	}
	if w := postAdminJSON(t, mux, "/admin/api/reports/review", input); w.Code != 200 {
		t.Fatalf("long rationale failed: %d %s", w.Code, w.Body.String())
	}
	if w := postAdminJSON(t, mux, "/admin/api/reports/review", input); w.Code != 409 {
		t.Fatal("stale review not rejected")
	}
	if w := get("/admin/api/reports/product/1", true); !strings.Contains(w.Body.String(), "measured evidence") {
		t.Fatal("review evidence was not retained")
	}
}
