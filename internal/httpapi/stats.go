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
		writeStoreErr(w, err, http.StatusInternalServerError, "stats lookup failed")
		return
	}
	if !ok {
		now := a.now()
		counts, cerr := a.d.Store.NetworkCounts(r.Context(), now)
		if cerr != nil {
			writeErr(w, http.StatusInternalServerError, "stats rollup failed")
			return
		}
		// A live rollup reads the adoption reports too; failing to read
		// them would publish a rate of zero, which is a claim rather than
		// a gap.
		adopt, aerr := a.d.Store.AdoptionSummary(r.Context())
		if aerr != nil {
			writeErr(w, http.StatusInternalServerError, "stats rollup failed")
			return
		}
		raw, jerr := compatibility.StatsJSON(counts, adopt, now)
		if jerr != nil {
			writeErr(w, http.StatusInternalServerError, "stats rollup failed")
			return
		}
		js = string(raw)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(w, a.withHotShards(r.Context(), js))
}
