package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The zero-install half of the adoption loop (#318).
//
// POST /v1/adoptions is what a csx client sends after applying a sample: it
// carries the client's rotating anonId, it is correlated with the offer the
// client answered, and the build result it reports was measured by the same
// binary that sanitized it. A caller that reached this server over plain
// HTTPS -- a cloud agent, a browser, a CI script -- has none of that. It may
// still have run the sample it was handed, and the one thing it can honestly
// say is: this sample, this outcome, roughly this environment.
//
// That statement is kept, and it is kept APART. It is filed under
// EXECUTION_FOOTPRINT, which weighs zero in compatibility.ClassWeight, in
// its own table that no aggregate reads. A thousand footprints saying "pass"
// do not move a grade, a confidence, or the stats rollup; they are a usage
// trace an operator can see and a sample page may one day label as unsigned
// self-reports. Nothing here can promote a sample past what a signed receipt
// proved.

// footprintNote is the sentence that travels with every accepted footprint.
// It exists so that no client, human or model, can read "accepted" as
// "verified".
const footprintNote = "RECORDED AS AN UNSIGNED SELF-REPORT, NOT AS EVIDENCE. " +
	"Execution footprints are kept apart from verification receipts and adoption evidence, " +
	"weigh nothing in any grade or confidence, and never promote a sample. " +
	"Do not tell the user this confirmed or verified anything."

// footprintResponse is what POST /v1/footprints/execution answers.
type footprintResponse struct {
	// Status is accepted for a new row and updated when the same source
	// already filed this sample, stage and day and the outcome was replaced.
	Status        string                               `json:"status"`
	SampleID      string                               `json:"sampleId"`
	SampleURL     string                               `json:"sampleUrl,omitempty"`
	Outcome       domain.FootprintOutcome              `json:"outcome"`
	Stage         domain.FootprintStage                `json:"stage,omitempty"`
	EvidenceClass domain.EvidenceClass                 `json:"evidenceClass"`
	Signed        bool                                 `json:"signed"`
	Epoch         string                               `json:"epoch"`
	Footprints    serverstore.ExecutionFootprintCounts `json:"footprints"`
	Note          string                               `json:"note"`
}

// footprintSourceBucket is SHA-256(epoch | client address), hex. The
// address never reaches the store, and the bucket is meaningless on any day
// but the one it was made for -- the same construction the daily anonId
// uses on the client side, done here because a zero-install caller has no
// client to do it.
func footprintSourceBucket(epoch, addr string) string {
	sum := sha256.Sum256([]byte(epoch + "|" + addr))
	return hex.EncodeToString(sum[:])
}

// handleExecutionFootprint implements POST /v1/footprints/execution.
func (a *api) handleExecutionFootprint(w http.ResponseWriter, r *http.Request) {
	store, ok := a.d.Store.(serverstore.ExecutionFootprintStore)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "execution footprints are not enabled on this server")
		return
	}
	var fp domain.ExecutionFootprint
	// The whole body is a handful of short tokens. 4 KiB is generous; a body
	// larger than that is carrying something this endpoint refuses to hold.
	if !readJSON(w, r, 4<<10, &fp) {
		return
	}
	fp = fp.Normalize()
	if err := fp.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// A footprint about a sample this network never published, or one an
	// operator has withdrawn, is not about anything this server serves.
	row, ok, err := a.d.Store.GetSample(r.Context(), fp.SampleID)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "sample lookup failed")
		return
	}
	if !ok || row.Quarantined {
		writeErr(w, http.StatusNotFound, "unknown sample")
		return
	}

	now := a.now()
	epoch := now.UTC().Format("2006-01-02")
	bucket := footprintSourceBucket(epoch, clientAddr(r))
	stored, duplicate, err := store.RecordExecutionFootprint(r.Context(), serverstore.ExecutionFootprintRow{
		DedupKey:           bucket + "/" + fp.SampleID + "/" + string(fp.Stage),
		SampleID:           fp.SampleID,
		Outcome:            fp.Outcome,
		Stage:              fp.Stage,
		Environment:        fp.Environment,
		FailureFingerprint: fp.FailureFingerprint,
		Epoch:              epoch,
		SourceBucket:       bucket,
	}, now)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "recording the footprint failed")
		return
	}
	counts, err := store.ExecutionFootprintCounts(r.Context(), fp.SampleID)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "reading footprints failed")
		return
	}
	resp := footprintResponse{
		Status:        "accepted",
		SampleID:      stored.SampleID,
		SampleURL:     a.sampleURL(stored.SampleID),
		Outcome:       stored.Outcome,
		Stage:         stored.Stage,
		EvidenceClass: domain.ClassExecutionFootprint,
		Signed:        false,
		Epoch:         stored.Epoch,
		Footprints:    counts,
		Note:          footprintNote,
	}
	if duplicate {
		resp.Status = "updated"
	}
	writeJSON(w, http.StatusOK, resp)
}

// sampleURL is the canonical page for a sample on this deployment, or ""
// when the deployment has not said where it is reachable. A relative path
// would be worse than nothing: an agent that got the JSON from one host and
// hands the link to a person needs the origin in it.
func (a *api) sampleURL(sampleID string) string {
	base := a.d.Cfg.PublicURL
	if base == "" {
		return ""
	}
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + "/samples/" + sampleID
}
