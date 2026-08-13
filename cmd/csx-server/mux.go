package main

import (
	"io"
	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// BuildMux assembles the csx-server HTTP handler.
//
// Wave C replaces this wiring with the real /v1 API and website handlers
// (internal/httpapi + internal/web); until then it exposes only the health
// probe, so this function stays the single obvious replacement seam.
func BuildMux(cfg serverstore.ServerConfig, store serverstore.Store) *http.ServeMux {
	_, _ = cfg, store // consumed by the Wave C API wiring
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	return mux
}
