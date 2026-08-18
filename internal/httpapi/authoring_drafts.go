package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

const authoringDraftTokenPrefix = "csx_author_v1_"

func authoringDraftTokenHash(r *http.Request) (string, bool) {
	token := bearerToken(r)
	if !strings.HasPrefix(token, authoringDraftTokenPrefix) {
		return "", false
	}
	encoded := strings.TrimPrefix(token, authoringDraftTokenPrefix)
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 || encoded != base64.RawURLEncoding.EncodeToString(raw) {
		return "", false
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:]), true
}

// handleAuthoringDraft stores a sample in the private operator inbox. It
// keeps LOCAL drafts private. A LOCAL_PASS draft becomes a quarantined DRAFT
// sample and a cross-verification job; only an independently signed PASS may
// atomically remove quarantine and publish it as CROSS_PASS.
func (a *api) handleAuthoringDraft(w http.ResponseWriter, r *http.Request) {
	store, ok := a.d.Store.(serverstore.AuthoringSessionStore)
	if !ok || a.d.Blobs == nil {
		writeErr(w, http.StatusServiceUnavailable, "authoring draft storage unavailable")
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
	// Authenticate before accepting any blob bytes. A submission is activity,
	// so it also extends the same one-hour idle lease as refresh.
	session, err := store.RefreshAuthoringSession(r.Context(), tokenHash, ip, "", now, now.Add(time.Hour))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "authoring session unavailable")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSampleReqBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	defer r.MultipartForm.RemoveAll()
	manifestJSON := r.FormValue("manifest")
	claimedID := r.FormValue("sampleId")
	localStatus := strings.ToUpper(strings.TrimSpace(r.FormValue("localStatus")))
	if manifestJSON == "" || claimedID == "" || (localStatus != "LOCAL" && localStatus != "LOCAL_PASS") {
		writeErr(w, http.StatusBadRequest, "manifest, sampleId and LOCAL or LOCAL_PASS status are required")
		return
	}
	var manifest domain.SampleManifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil || manifest.SchemaVersion != 1 ||
		len(manifest.ContractCommand) == 0 || manifest.VerifierAdapter == "" {
		writeErr(w, http.StatusBadRequest, "invalid sample manifest")
		return
	}
	work, assigned, err := store.AuthoringWorkForSubmission(r.Context(), session.SessionID, claimedID, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "authoring work lookup failed")
		return
	}
	if !assigned || !manifestAnswersAuthoringWork(manifest, work) {
		writeErr(w, http.StatusConflict, "sample does not match this worker's assigned Wanted item")
		return
	}
	license := manifest.License
	if license == "" {
		license = "MIT-0"
	}
	if !permissiveLicenses[license] {
		writeErr(w, http.StatusBadRequest, "sample license is not permitted")
		return
	}
	file, _, err := r.FormFile("artifact")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "artifact file field is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxArtifactBytes+1))
	if err != nil || len(data) > maxArtifactBytes || domain.SHA256Hex(data) != claimedID {
		writeErr(w, http.StatusBadRequest, "invalid artifact or sampleId")
		return
	}
	if err := checkArtifactStatic(data, manifest); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if have, herr := a.d.Blobs.Has(r.Context(), claimedID); herr == nil && !have {
		if full, _ := a.blobBudgetExceeded(r.Context()); full {
			writeErr(w, http.StatusInsufficientStorage, "sample storage is at its configured budget")
			return
		}
	}
	blobID, err := a.d.Blobs.Put(r.Context(), bytes.NewReader(data))
	if err != nil || blobID != claimedID {
		writeErr(w, http.StatusInternalServerError, "storing draft artifact failed")
		return
	}
	// Consume the still-live Wanted lease before creating any verification
	// state. A lease that expires between authentication and upload must not
	// leave a publishable sample/job behind. Once attached, an idempotent retry
	// by this same session remains authorized even if a later store step fails.
	if localStatus == "LOCAL_PASS" && work.SampleID == "" {
		attached, attachErr := store.AttachAuthoringWorkSample(r.Context(), session.SessionID, work, claimedID, now)
		if attachErr != nil || !attached {
			writeErr(w, http.StatusConflict, "authoring work lease expired before submission completed")
			return
		}
	}
	manifest.License = license
	if err := store.SaveAuthoringDraft(r.Context(), serverstore.AuthoringDraftRow{
		SampleID: claimedID, SessionID: session.SessionID, WorkerLabel: session.Label,
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)), LocalStatus: localStatus,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "saving authoring draft failed")
		return
	}
	responseStatus := "PRIVATE_DRAFT"
	if localStatus == "LOCAL_PASS" {
		manifest.Case.CaseID = manifest.Case.ComputeID()
		if err := a.d.Store.SaveCase(r.Context(), manifest.Case); err != nil {
			writeErr(w, http.StatusInternalServerError, "saving draft case failed")
			return
		}
		if err := a.d.Store.SaveSample(r.Context(), serverstore.SampleRow{
			SampleID: claimedID, CaseID: manifest.Case.CaseID,
			ManifestJSON: string(domain.MustCanonicalJSON(manifest)), Status: "DRAFT",
			License: license, SizeBytes: int64(len(data)), CreatedAt: now,
			Quarantined: true, QuarantineReason: "private authoring draft awaiting cross verification",
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, "saving private verification sample failed")
			return
		}
		if err := a.queueCrossVerification(r.Context(), claimedID); err != nil {
			writeErr(w, http.StatusInternalServerError, "queueing draft verification failed")
			return
		}
		responseStatus = "CROSS_PENDING"
		if published, ok, lookupErr := a.d.Store.GetSample(r.Context(), claimedID); lookupErr == nil && ok && !published.Quarantined &&
			(published.Status == "CROSS_PASS" || published.Status == "MATRIX_PASS" || published.Status == "STABLE") {
			responseStatus = published.Status
		}
	}
	writeJSON(w, http.StatusCreated, map[string]string{"sampleId": claimedID, "status": responseStatus})
}

func manifestAnswersAuthoringWork(manifest domain.SampleManifest, work serverstore.AuthoringWorkRow) bool {
	packageMatch := false
	for _, raw := range manifest.Packages {
		p, err := domain.ParsePURL(raw)
		if err == nil && p.Ecosystem == work.Ecosystem && p.Name == work.Name && p.Version == work.Version {
			packageMatch = true
			break
		}
	}
	if !packageMatch || work.Symbol == "" {
		return packageMatch
	}
	for _, symbol := range manifest.Symbols {
		if symbol == work.Symbol {
			return true
		}
	}
	return false
}
