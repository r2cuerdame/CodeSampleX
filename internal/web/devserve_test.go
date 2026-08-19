package web

import (
	"net/http"
	"os"
	"testing"
)

// TestDevServe runs the site live on CSX_WEB_DEVSERVE (an addr like
// "127.0.0.1:8932") against the cube fixture store, so the explorer's
// drill-down can be clicked through in a real browser. Skipped unless the
// env var is set; run with -timeout 0 and stop with Ctrl-C.
func TestDevServe(t *testing.T) {
	addr := os.Getenv("CSX_WEB_DEVSERVE")
	if addr == "" {
		t.Skip("CSX_WEB_DEVSERVE not set")
	}
	mux, _ := newTestMux(t, func(d *Deps) {
		d.Store = matrixStore()
		// Derive the origin from the request so every canonical and
		// install link stays on the local host.
		d.PublicURL = ""
	})
	t.Logf("serving on http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		t.Fatal(err)
	}
}
