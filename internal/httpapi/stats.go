package httpapi

import (
	"io"
	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
)

// handleStats implements GET /v1/stats: the latest builder-generated daily
// rollup. Before the first builder pass it computes a live rollup so the
// endpoint never 404s. estimatedReasoningAvoided ALWAYS carries
// "estimated": true — the dashboard never presents an estimate as a
// measurement.
func (a *api) handleStats(w http.ResponseWriter, r *http.Request) {
	js, ok, err := a.d.Store.GetLatestStats(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stats lookup failed")
		return
	}
	if !ok {
		now := a.now()
		counts, cerr := a.d.Store.NetworkCounts(r.Context(), now)
		if cerr != nil {
			writeErr(w, http.StatusInternalServerError, "stats rollup failed")
			return
		}
		raw, jerr := compatibility.StatsJSON(counts, 0, now)
		if jerr != nil {
			writeErr(w, http.StatusInternalServerError, "stats rollup failed")
			return
		}
		js = string(raw)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(w, a.withHotShards(r.Context(), js))
}
