package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// stableWindow: STABLE requires no FAIL receipt within this window (C13).
const stableWindow = 30 * 24 * time.Hour

// handleVerification implements POST /v1/verifications: verify the ed25519
// signature and the peerId↔pubkey binding, persist the immutable receipt,
// then recompute the sample's status per the BINDING transition rules:
//
//	PUBLISHED   → CROSS_PASS   first contract-PASS receipt from a peer ≠ origin
//	            → MATRIX_PASS  receipts span ≥2 distinct context boundaries
//	                           (os OR runtime major OR browserFamily)
//	            → STABLE       ≥3 distinct passing peers ∧ no FAIL in 30d
func (a *api) handleVerification(w http.ResponseWriter, r *http.Request) {
	var receipt domain.VerificationReceipt
	if !readJSON(w, r, 1<<20, &receipt) {
		return
	}
	if receipt.SchemaVersion != 1 {
		writeErr(w, http.StatusBadRequest, "receipt schemaVersion must be 1")
		return
	}
	if receipt.SampleID == "" || receipt.PeerID == "" || receipt.PeerPubkey == "" || receipt.PeerSignature == "" {
		writeErr(w, http.StatusBadRequest, "receipt requires sampleId, peerId, peerPubkey and peerSignature")
		return
	}

	// Signature over the canonical receipt (signature field cleared).
	if !identity.Verify(receipt.PeerPubkey, receipt.PeerSignature, receipt.SigningBytes()) {
		writeErr(w, http.StatusBadRequest, "invalid receipt signature")
		return
	}
	// peerId must be the fingerprint of the embedded pubkey — a valid
	// signature under someone else's name is still a forgery.
	if derivePeerID(receipt.PeerPubkey) != receipt.PeerID {
		writeErr(w, http.StatusBadRequest, "peerId does not match peerPubkey fingerprint")
		return
	}

	ctx := r.Context()
	sample, ok, err := a.d.Store.GetSample(ctx, receipt.SampleID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sample lookup failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown sample")
		return
	}

	contractResult := receipt.Stages["contract"]
	if err := a.d.Store.SaveReceipt(ctx, serverstore.ReceiptRow{
		ReceiptID:      receipt.ReceiptID(),
		SampleID:       receipt.SampleID,
		PeerID:         receipt.PeerID,
		EnvHash:        receipt.EnvironmentHash,
		ReceiptJSON:    string(domain.MustCanonicalJSON(receipt)),
		ContractResult: contractResult,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "saving receipt failed")
		return
	}

	receipts, err := a.d.Store.ReceiptsForSample(ctx, receipt.SampleID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "receipt lookup failed")
		return
	}
	newStatus := sampleStatusFromReceipts(sample.Status, receipts, a.now())
	if newStatus != sample.Status {
		if err := a.d.Store.SetSampleStatus(ctx, receipt.SampleID, newStatus); err != nil {
			writeErr(w, http.StatusInternalServerError, "updating sample status failed")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":       "accepted",
		"sampleStatus": newStatus,
	})
}

// derivePeerID computes "ed25519:" + hex(sha256(pubkey))[:16].
func derivePeerID(pubB64 string) string {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(pub)
	return "ed25519:" + hex.EncodeToString(sum[:])[:16]
}

// sampleStatusFromReceipts applies the C13 transition rules. Status only
// ever upgrades; the first receipt's peer is the origin.
func sampleStatusFromReceipts(current string, rows []serverstore.ReceiptRow, now time.Time) string {
	if len(rows) == 0 {
		return current
	}
	origin := rows[0].PeerID

	crossPass := false
	passPeers := map[string]bool{}
	recentFail := false
	var infos []compatibility.ReceiptInfo
	for _, row := range rows {
		switch row.ContractResult {
		case string(domain.ResultPass):
			passPeers[row.PeerID] = true
			if row.PeerID != origin {
				crossPass = true
			}
			if info, ok := compatibility.ParseReceiptRow(row); ok {
				infos = append(infos, info)
			}
		case string(domain.ResultFail):
			if row.CreatedAt.IsZero() || now.Sub(row.CreatedAt) <= stableWindow {
				recentFail = true
			}
		}
	}

	status := current
	rank := statusRank(status)
	if crossPass && rank < statusRank("CROSS_PASS") {
		status, rank = "CROSS_PASS", statusRank("CROSS_PASS")
	}
	if crossPass && spansContextBoundary(infos) && rank < statusRank("MATRIX_PASS") {
		status, rank = "MATRIX_PASS", statusRank("MATRIX_PASS")
	}
	if len(passPeers) >= 3 && !recentFail && rank < statusRank("STABLE") {
		status = "STABLE"
	}
	return status
}

func statusRank(status string) int {
	switch status {
	case "STABLE":
		return 4
	case "MATRIX_PASS":
		return 3
	case "CROSS_PASS":
		return 2
	case "PUBLISHED":
		return 1
	}
	return 0
}

// spansContextBoundary reports whether passing receipts cover ≥2 distinct
// values of at least one context boundary: os, runtime major, or browser
// family (docs/execution-context.md §4).
func spansContextBoundary(infos []compatibility.ReceiptInfo) bool {
	oses := map[string]bool{}
	runtimeMajors := map[string]bool{}
	browsers := map[string]bool{}
	for _, info := range infos {
		env := info.Env
		if env.OS != "" {
			oses[env.OS] = true
		}
		if env.Runtime != "" {
			runtimeMajors[env.Runtime+"@"+majorSeg(env.RuntimeVersion)] = true
		}
		if env.BrowserFamily != "" {
			browsers[env.BrowserFamily] = true
		}
	}
	return len(oses) >= 2 || len(runtimeMajors) >= 2 || len(browsers) >= 2
}

// handleJobsList implements GET /v1/verification/jobs?peerId=&capability=&limit=.
func (a *api) handleJobsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	capability := q.Get("capability")
	limit := 10
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	jobs, err := a.d.Store.OpenJobs(r.Context(), capability, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "job listing failed")
		return
	}
	type jobOut struct {
		ID       int64           `json:"id"`
		SampleID string          `json:"sampleId"`
		Reason   string          `json:"reason"`
		WantEnv  json.RawMessage `json:"wantEnv,omitempty"`
	}
	out := []jobOut{}
	for _, j := range jobs {
		jo := jobOut{ID: j.ID, SampleID: j.SampleID, Reason: j.Reason}
		if j.WantEnvJSON != "" {
			jo.WantEnv = json.RawMessage(j.WantEnvJSON)
		}
		out = append(out, jo)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

// handleJobClaim implements POST /v1/verification/jobs/{id}/claim {peerId}.
func (a *api) handleJobClaim(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid job id")
		return
	}
	var body struct {
		PeerID string `json:"peerId"`
	}
	if !readJSON(w, r, 64<<10, &body) {
		return
	}
	if !validPeerID(body.PeerID) {
		writeErr(w, http.StatusBadRequest, "peerId must be \"ed25519:<16 hex>\"")
		return
	}
	claimed, err := a.d.Store.ClaimJob(r.Context(), id, body.PeerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "claim failed")
		return
	}
	if !claimed {
		writeErr(w, http.StatusConflict, "job already claimed or not open")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "claimed", "id": id})
}

// validPeerID checks the "ed25519:<16 lowercase hex>" fingerprint form.
func validPeerID(peerID string) bool {
	const prefix = "ed25519:"
	if len(peerID) != len(prefix)+16 || peerID[:len(prefix)] != prefix {
		return false
	}
	for i := len(prefix); i < len(peerID); i++ {
		c := peerID[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
