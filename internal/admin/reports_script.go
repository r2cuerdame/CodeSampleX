package admin

import (
	_ "embed"
	"net/http"
)

//go:embed static/reports.js
var reportsJS []byte

func (h *handler) reportsScript(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if !h.authorized(r) {
		http.Error(w, "인증이 필요합니다", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	if r.Method != http.MethodHead {
		_, _ = w.Write(reportsJS)
	}
}
