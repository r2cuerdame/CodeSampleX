package httpapi

import (
	"net/http"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/activity"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const authoringWorkLease = 24 * time.Hour

var authoringSupportedEcosystems = map[string]bool{
	"npm": true, "pypi": true, "golang": true, "cargo": true,
	"composer": true, "gem": true, "pub": true, "hex": true, "maven": true,
}

func (a *api) handleAuthoringWorkNext(w http.ResponseWriter, r *http.Request) {
	store, ok := a.d.Store.(serverstore.AuthoringSessionStore)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "authoring work storage unavailable")
		return
	}
	tokenHash, ok := authoringDraftTokenHash(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "authoring session unavailable")
		return
	}
	now := a.now().UTC()
	ip := ""
	if addr, ok := activity.ExternalRequestAddress(r); ok {
		ip = addr.String()
	}
	session, err := store.RefreshAuthoringSession(r.Context(), tokenHash, ip, "", now, now.Add(time.Hour))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "authoring session unavailable")
		return
	}
	candidates, err := a.d.Store.TopWanted(r.Context(), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "listing authoring work failed")
		return
	}
	eligible := candidates[:0]
	for _, candidate := range candidates {
		if candidate.Version != "" && authoringSupportedEcosystems[candidate.Ecosystem] {
			eligible = append(eligible, candidate)
		}
	}
	work, found, err := store.ClaimAuthoringWork(r.Context(), session.SessionID, eligible, now, now.Add(authoringWorkLease))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "claiming authoring work failed")
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, map[string]string{"status": "NO_WORK"})
		return
	}
	purl := domain.PURL{Ecosystem: work.Ecosystem, Name: work.Name, Version: work.Version}.String()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ASSIGNED",
		"work": map[string]any{
			"ecosystem": work.Ecosystem, "name": work.Name, "version": work.Version,
			"symbol": work.Symbol, "asks": work.Asks, "package": purl,
			"leaseExpiresAt": work.LeaseExpiresAt.UTC(),
		},
	})
}
