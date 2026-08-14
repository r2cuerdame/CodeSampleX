package httpapi

import (
	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// adoptionReport is what a peer sends after applying a sample.
//
// It carries no query, no source and no project identity — only the sample
// it is about, whether it was used, and whether the build then passed. The
// anonId is the same rotating daily identifier the evidence batches use.
type adoptionReport struct {
	SchemaVersion int    `json:"schemaVersion"`
	EvidenceClass string `json:"evidenceClass"`
	Epoch         string `json:"epoch"`
	AnonID        string `json:"anonId"`
	SampleID      string `json:"sampleId"`
	Applied       bool   `json:"applied"`
	BuildPass     *bool  `json:"buildPass,omitempty"`
}

// handleAdoption implements POST /v1/adoptions.
//
// This route did not exist. The client enqueued every adoption report into
// its local upload_queue, nothing ever drained that table, and there was
// nowhere to send one — so the far end of the loop the whole product
// describes (ask, get a verified answer, report whether it worked) was
// never connected, and postHitSuccessRate was a hardcoded 0 with a comment
// explaining why.
//
// Anonymous like every other write here. What keeps it honest is the
// primary key: one reporter counts once per sample per epoch, so an agent
// retrying all afternoon is one report. A repeat within the epoch updates
// the outcome rather than adding a row — an agent that reports "applied"
// and later reports the build result is telling us more about the same
// event, not about a second one.
func (a *api) handleAdoption(w http.ResponseWriter, r *http.Request) {
	var req adoptionReport
	if !readJSON(w, r, 1<<16, &req) {
		return
	}
	if req.SchemaVersion != 1 {
		writeErr(w, http.StatusBadRequest, "adoption report schemaVersion must be 1")
		return
	}
	if req.SampleID == "" || req.Epoch == "" || req.AnonID == "" {
		writeErr(w, http.StatusBadRequest, "adoption report requires sampleId, epoch and anonId")
		return
	}
	// An adoption of a sample this network never published is not evidence
	// about anything it can serve.
	if _, ok, err := a.d.Store.GetSample(r.Context(), req.SampleID); err != nil {
		writeErr(w, http.StatusInternalServerError, "sample lookup failed")
		return
	} else if !ok {
		writeErr(w, http.StatusNotFound, "unknown sample")
		return
	}

	if err := a.d.Store.RecordAdoption(r.Context(), serverstore.AdoptionRow{
		SampleID:  req.SampleID,
		Applied:   req.Applied,
		BuildPass: req.BuildPass,
		Epoch:     req.Epoch,
		AnonID:    req.AnonID,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "recording the report failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}
