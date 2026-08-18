package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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

type authoringWorkRequest struct {
	SchemaVersion     int                      `json:"schemaVersion"`
	SandboxCapability domain.SandboxCapability `json:"sandboxCapability"`
	VerifierOS        []string                 `json:"verifierOS"`
}

func readAuthoringWorkRequest(w http.ResponseWriter, r *http.Request) (authoringWorkRequest, bool) {
	// v0.1.18 and older send an empty body. Their verifier adapters are all
	// pinned Linux containers, so preserve compatibility without pretending
	// the Windows host itself is the execution target.
	request := authoringWorkRequest{SchemaVersion: 1, SandboxCapability: domain.CapContainerRun, VerifierOS: []string{"linux"}}
	if r.ContentLength == 0 {
		return request, true
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid authoring environment")
		return authoringWorkRequest{}, false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid authoring environment")
		return authoringWorkRequest{}, false
	}
	if request.SchemaVersion != 1 || (request.SandboxCapability != domain.CapContainerRun && request.SandboxCapability != domain.CapCompileOnly) || len(request.VerifierOS) > 4 {
		writeErr(w, http.StatusBadRequest, "unsupported authoring environment")
		return authoringWorkRequest{}, false
	}
	seen := map[string]bool{}
	for i, targetOS := range request.VerifierOS {
		targetOS = strings.ToLower(strings.TrimSpace(targetOS))
		if targetOS != "linux" {
			writeErr(w, http.StatusBadRequest, "unsupported authoring environment")
			return authoringWorkRequest{}, false
		}
		request.VerifierOS[i] = targetOS
		seen[targetOS] = true
	}
	if request.SandboxCapability == domain.CapContainerRun && !seen["linux"] {
		writeErr(w, http.StatusBadRequest, "unsupported authoring environment")
		return authoringWorkRequest{}, false
	}
	return request, true
}

func authoringCandidateEligible(candidate serverstore.WantedRow, request authoringWorkRequest) bool {
	if request.SandboxCapability != domain.CapContainerRun || candidate.Version == "" || !authoringSupportedEcosystems[candidate.Ecosystem] {
		return false
	}
	if candidate.Kind == "WANTED" {
		return true
	}
	if candidate.Kind != "FINDING" && candidate.Kind != "EXPANSION" {
		return false
	}
	for _, targetOS := range request.VerifierOS {
		if strings.EqualFold(candidate.TargetOS, targetOS) {
			return true
		}
	}
	return false
}

func (a *api) handleAuthoringWorkNext(w http.ResponseWriter, r *http.Request) {
	store, ok := a.d.Store.(serverstore.AuthoringSessionStore)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "authoring work storage unavailable")
		return
	}
	request, ok := readAuthoringWorkRequest(w, r)
	if !ok {
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
	eligible := make([]serverstore.WantedRow, 0, 400)
	for _, candidate := range candidates {
		candidate.Kind = "WANTED"
		candidate.Score = candidate.Asks
		if authoringCandidateEligible(candidate, request) {
			eligible = append(eligible, candidate)
		}
	}
	expansion, err := store.ListAuthoringExpansionCandidates(r.Context(), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "listing authoring expansion work failed")
		return
	}
	for _, candidate := range expansion {
		if authoringCandidateEligible(candidate, request) {
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
			"symbol": work.Symbol, "asks": work.Asks, "kind": work.Kind, "score": work.Score, "package": purl,
			"leaseExpiresAt": work.LeaseExpiresAt.UTC(),
		},
	})
}
