package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// BuilderFreshnessLimit is how old the stats rollup may be before the builder
// is reported stale (#517). It is resumeWindow's day: past it, the builder
// has not finished anything for longer than it can resume across.
const BuilderFreshnessLimit = 24 * time.Hour

// builderStatusResponse is GET /v1/builder: the builder's own record of its
// last pass, plus the two ages a monitor needs without doing date arithmetic.
type builderStatusResponse struct {
	compatibility.BuilderStatus
	// Recorded is false when no pass has written a record yet (or this
	// store keeps none); every other builder field is then empty.
	Recorded              bool   `json:"recorded"`
	LastSuccessAgeSeconds *int64 `json:"lastSuccessAgeSeconds,omitempty"`
	// StatsGeneratedAt is the stamp GET /v1/stats carries, and its age. It is
	// what the site displays, so it is what staleness is judged on.
	StatsGeneratedAt  string `json:"statsGeneratedAt,omitempty"`
	StatsAgeSeconds   *int64 `json:"statsAgeSeconds,omitempty"`
	StaleAfterSeconds int64  `json:"staleAfterSeconds"`
	Stale             bool   `json:"stale"`
	ObservedAt        string `json:"observedAt"`
}

func ageSeconds(now time.Time, stamp string) *int64 {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return nil
	}
	age := int64(now.Sub(t) / time.Second)
	return &age
}

// handleBuilderStatus implements GET /v1/builder. The failure reason is a
// closed set (timeout, lease_lost, canceled, error) plus the phase and a
// coarse error class; the error text itself stays in the server log.
func (a *api) handleBuilderStatus(w http.ResponseWriter, r *http.Request) {
	now := a.now()
	resp := builderStatusResponse{
		StaleAfterSeconds: int64(BuilderFreshnessLimit / time.Second),
		ObservedAt:        now.UTC().Format(time.RFC3339),
	}
	if store, ok := a.d.Store.(serverstore.BuilderStatusStore); ok {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), latestStatsReadTimeout)
		js, found, err := store.GetBuilderStatus(loadCtx, compatibility.BuilderStatusName)
		cancel()
		if err != nil {
			writeStoreErr(w, err, http.StatusInternalServerError, "builder status lookup failed")
			return
		}
		if found && json.Unmarshal([]byte(js), &resp.BuilderStatus) == nil {
			resp.Recorded = true
			resp.LastSuccessAgeSeconds = ageSeconds(now, resp.LastSuccessAt)
		}
	}
	stats, found, err := a.latestStats(r.Context())
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "stats lookup failed")
		return
	}
	if found {
		var doc struct {
			GeneratedAt string `json:"generatedAt"`
		}
		if json.Unmarshal([]byte(stats), &doc) == nil {
			resp.StatsGeneratedAt = doc.GeneratedAt
			resp.StatsAgeSeconds = ageSeconds(now, doc.GeneratedAt)
		}
	}
	// No stamp at all is as stale as a stamp can be: nothing has ever
	// finished that the site could show.
	resp.Stale = resp.StatsAgeSeconds == nil || *resp.StatsAgeSeconds > resp.StaleAfterSeconds
	writeJSON(w, http.StatusOK, resp)
}
