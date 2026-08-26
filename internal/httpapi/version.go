package httpapi

import (
	"net/http"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/buildinfo"
)

// versionResponse is the machine-readable form of the identity the site
// footer shows. Same process, same struct, one source: a deploy check that
// reads this and a human who reads the footer cannot disagree.
type versionResponse struct {
	Service       string `json:"service"`
	Version       string `json:"version,omitempty"`
	Revision      string `json:"revision,omitempty"`
	ShortRevision string `json:"shortRevision,omitempty"`
	Environment   string `json:"environment"`
	BuiltAt       string `json:"builtAt,omitempty"`
}

// serviceName distinguishes this build from the csx client's own release
// version, which is a different artifact on a different release cadence.
const serviceName = "csx-server"

// handleVersion answers what is running here.
//
// It reads nothing but the process's own build stamps: no database, no blob
// store, no clock. That is deliberate. This is the endpoint a deployment
// asks "did the commit I shipped actually start serving", and an answer that
// can fail for an unrelated reason cannot be used to decide that. /healthz
// stays the endpoint that proves the database is reachable.
func (a *api) handleVersion(w http.ResponseWriter, r *http.Request) {
	// A cached identity is a wrong identity the moment a rollout finishes.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, versionPayload(a.d.Build))
}

func versionPayload(info buildinfo.Info) versionResponse {
	out := versionResponse{
		Service:     serviceName,
		Version:     info.Version,
		Revision:    info.Revision,
		Environment: info.Environment,
	}
	out.ShortRevision = info.ShortRevision()
	if !info.BuiltAt.IsZero() {
		out.BuiltAt = info.BuiltAt.UTC().Format(time.RFC3339)
	}
	return out
}
