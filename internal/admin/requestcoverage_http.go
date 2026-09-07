package admin

import (
	"mime"
	"net/http"
)

func (h *handler) requestCoverageScript(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	body, err := adminStaticFS.ReadFile("static/request-coverage.js")
	if err != nil {
		http.Error(w, "script unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(".js"))
	w.Header().Set("Content-Length", stringInt(len(body)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}
