package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAdminAuthRejectsAnInvalidDigest proves AdminAuth applies the same
// "no route rather than an unauthorized one" gate Register applies to
// /admin itself: a caller with no valid configured credential should not
// mount its route at all.
func TestAdminAuthRejectsAnInvalidDigest(t *testing.T) {
	for _, hash := range []string{"", "raw-token", "not-hex"} {
		if _, ok := AdminAuth(hash, nil); ok {
			t.Fatalf("hash %q: AdminAuth ok = true, want false", hash)
		}
	}
}

// TestAdminAuthGatesTheWrappedHandler proves AdminAuth's middleware enforces
// exactly the credential /admin's own handler.authorized does -- Basic auth
// with username "recuerdame" against the digest -- and never runs the
// wrapped handler when that check fails.
func TestAdminAuthGatesTheWrappedHandler(t *testing.T) {
	secret := "adminauth-test-secret"
	sum := sha256.Sum256([]byte(secret))
	mw, ok := AdminAuth(hex.EncodeToString(sum[:]), nil)
	if !ok {
		t.Fatal("AdminAuth ok = false with a valid digest")
	}

	ran := false
	wrapped := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	}))

	unauth := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, unauth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: status = %d, want 401", rec.Code)
	}
	if ran {
		t.Fatal("the wrapped handler ran without a valid credential")
	}

	authed := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	authed.SetBasicAuth("recuerdame", secret)
	rec2 := httptest.NewRecorder()
	wrapped.ServeHTTP(rec2, authed)
	if rec2.Code != http.StatusOK {
		t.Fatalf("valid credentials: status = %d, want 200", rec2.Code)
	}
	if !ran {
		t.Fatal("the wrapped handler did not run with a valid credential")
	}
}
