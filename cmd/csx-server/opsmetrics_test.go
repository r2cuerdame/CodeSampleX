package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/web"
)

// TestBuildMuxOpsMetricsRouteIsAbsentUntilValidHashConfigured mirrors
// TestBuildMuxAdminRouteIsAbsentUntilValidHashConfigured: with no valid
// admin credential configured, GET /v1/ops/pool-metrics must be
// indistinguishable from an unknown path, not merely refuse every request
// that reaches it.
func TestBuildMuxOpsMetricsRouteIsAbsentUntilValidHashConfigured(t *testing.T) {
	t.Setenv("CSX_ADMIN_ACCESS_LOG", "")
	for _, tokenHash := range []string{"", "raw-token", "not-hex"} {
		mux := BuildMux(serverstore.ServerConfig{AdminTokenSHA256: tokenHash}, serverstore.NewFake())
		req := httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("hash %q: /v1/ops/pool-metrics status = %d, want 404", tokenHash, rec.Code)
		}
	}
}

// TestBuildMuxOpsMetricsRouteRequiresAdminAuth proves GET
// /v1/ops/pool-metrics is genuinely behind the same admin authentication
// /admin uses: refused without credentials, served with the correct Basic
// credential, and shaped as the documented pool/host/farmIngest JSON
// contract. Mirrors TestBuildMuxWiresPrivateAdminRoute's assertion style.
func TestBuildMuxOpsMetricsRouteRequiresAdminAuth(t *testing.T) {
	t.Setenv("CSX_ADMIN_ACCESS_LOG", "")
	secret := "integration-ops-metrics-secret"
	sum := sha256.Sum256([]byte(secret))
	cfg := serverstore.ServerConfig{AdminTokenSHA256: hex.EncodeToString(sum[:])}
	mux := BuildMux(cfg, serverstore.NewFake())

	unauth := httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil)
	unauthRec := httptest.NewRecorder()
	mux.ServeHTTP(unauthRec, unauth)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: status = %d, want 401; body=%s", unauthRec.Code, unauthRec.Body.String())
	}

	wrongAuth := httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil)
	wrongAuth.SetBasicAuth("recuerdame", "wrong-secret")
	wrongRec := httptest.NewRecorder()
	mux.ServeHTTP(wrongRec, wrongAuth)
	if wrongRec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", wrongRec.Code)
	}

	authed := httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil)
	authed.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex, nofollow, noarchive" {
		t.Fatalf("X-Robots-Tag = %q", got)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v (body=%s)", err, rec.Body.String())
	}
	for _, field := range []string{"pool", "host", "farmIngest", "routes"} {
		if _, ok := body[field]; !ok {
			t.Fatalf("response missing top-level field %q: %s", field, rec.Body.String())
		}
	}
	// The route ledger (#445) is wired from the website's own counters, so
	// production can read "timeout -> 404 = 0" off this endpoint. A 404 the
	// website served is a proven one and must show up as exactly that.
	routes, _ := body["routes"].(map[string]any)
	if routes["measured"] != true {
		t.Fatalf("routes.measured = %v, want true (wired from internal/web); body=%s", routes["measured"], rec.Body.String())
	}
	web.ResetRouteMetrics()
	missing := httptest.NewRequest(http.MethodGet, "/npm/completely-absent-package", nil)
	mux.ServeHTTP(httptest.NewRecorder(), missing)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authed)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	routes, _ = body["routes"].(map[string]any)
	if routes["provenNotFound"] != float64(1) || routes["final503"] != float64(0) {
		t.Fatalf("routes after one proven 404 = %#v, want provenNotFound=1 final503=0", routes)
	}
}
