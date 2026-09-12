package httpapi

import (
	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// maxPresenceBodyBytes bounds the presence report payload to 4KB.
// The valid payload is ~250 bytes; anything larger is rejected.
const maxPresenceBodyBytes = 4096

// handlePresence implements POST /v1/presence (GitHub #383).
//
// Records an anonymous presence ping for active installation measurement.
// Strict bounded JSON, validated via domain.PresencePayload.Validate(),
// stored idempotently in serverstore.
//
// Privacy contract:
// - Does not persist or use IP or User-Agent for identity.
// - Tokens are domain-separated rotating HMAC hashes derived locally from anonSeed.
// - Stable only inside their own aligned epoch (1d, 7d, 30d blocks) and unlinkable across epochs.
// - Intentionally measures current aligned 1/7/30-day windows, NOT sliding DAU/WAU/MAU.
// - Never represents people, users, or MAU.
func (a *api) handlePresence(w http.ResponseWriter, r *http.Request) {
	var req domain.PresencePayload
	if !readJSON(w, r, maxPresenceBodyBytes, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.d.Store.RecordPresence(r.Context(), req, a.now()); err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "recording presence failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}
