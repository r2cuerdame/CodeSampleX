package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestBuildMuxAdminRouteIsAbsentUntilValidHashConfigured(t *testing.T) {
	t.Setenv("CSX_ADMIN_ACCESS_LOG", "")
	for _, tokenHash := range []string{"", "raw-token", "not-hex"} {
		mux := BuildMux(serverstore.ServerConfig{AdminTokenSHA256: tokenHash}, serverstore.NewFake())
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("hash %q: /admin status = %d, want 404", tokenHash, rec.Code)
		}
	}
}

func TestBuildMuxWiresPrivateAdminRoute(t *testing.T) {
	t.Setenv("CSX_ADMIN_ACCESS_LOG", "")
	secret := "integration-admin-secret"
	sum := sha256.Sum256([]byte(secret))
	cfg := serverstore.ServerConfig{AdminTokenSHA256: hex.EncodeToString(sum[:])}
	mux := BuildMux(cfg, serverstore.NewFake())

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("admin", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/admin status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex, nofollow, noarchive" {
		t.Fatalf("X-Robots-Tag = %q", got)
	}
}
