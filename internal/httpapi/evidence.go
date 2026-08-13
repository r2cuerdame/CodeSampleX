package httpapi

import (
	"net/http"
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const (
	maxBatchesPerRequest = 500
	maxEvidenceBody      = 2 << 20 // 2MB
)

// handleEvidenceBatches implements POST /v1/evidence/batches: validate each
// batch, gate on publicness (goal.md §8.1 — private/unknown packages never
// enter the network), then delta-merge accepted batches. Only fingerprints
// and machine codes are stored; raw error text never reaches storage
// (enforced by serverstore.ValidateBatch).
func (a *api) handleEvidenceBatches(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Batches []domain.ObservationBatch `json:"batches"`
	}
	if !readJSON(w, r, maxEvidenceBody, &req) {
		return
	}
	if len(req.Batches) == 0 {
		writeErr(w, http.StatusBadRequest, "no batches in request")
		return
	}
	if len(req.Batches) > maxBatchesPerRequest {
		writeErr(w, http.StatusBadRequest, "too many batches in one request (max 500)")
		return
	}

	rejected := []serverstore.RejectedBatch{}
	var keep []domain.ObservationBatch
	var keepIdx []int
	for i, b := range req.Batches {
		if err := serverstore.ValidateBatch(b); err != nil {
			rejected = append(rejected, serverstore.RejectedBatch{Index: i, Reason: err.Error()})
			continue
		}
		if !a.trustMode() {
			p, _ := domain.ParsePURL(b.Package) // parse checked by ValidateBatch
			status := scanner.PublicnessUnknown
			if a.d.Checker != nil {
				status = a.d.Checker.Check(r.Context(), p)
			}
			if status != scanner.PublicnessPublic {
				// UNKNOWN is treated as private — the safe default (§25.E).
				rejected = append(rejected, serverstore.RejectedBatch{
					Index: i, Reason: "package is not public (" + status + ")",
				})
				continue
			}
		}
		keep = append(keep, b)
		keepIdx = append(keepIdx, i)
	}

	accepted := 0
	if len(keep) > 0 {
		acc, rej, err := a.d.Store.IngestBatches(r.Context(), keep)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "ingest failed")
			return
		}
		accepted = acc
		for _, rb := range rej { // map filtered indices back to request order
			rb.Index = keepIdx[rb.Index]
			rejected = append(rejected, rb)
		}
	}
	sort.Slice(rejected, func(i, j int) bool { return rejected[i].Index < rejected[j].Index })

	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": accepted,
		"rejected": rejected,
	})
}
