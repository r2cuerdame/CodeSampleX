package web

import (
	"net/http"
	"testing"
)

// /contribute was retired with no redirect, and its address is printed in
// nine READMEs' history, old MCP replies and external links. A retired URL
// that people were sent to gets a redirect, not a 404 — and the nearest
// living answer to "how do I contribute" is the request board: installing
// csx already contributes evidence, and /wanted is where the asks land.
func TestRetiredContributeURLRedirects(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/contribute")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/wanted" {
		t.Errorf("Location = %q, want /wanted", loc)
	}
}
