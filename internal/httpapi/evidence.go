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
	// maxRegistryLookupsPerRequest bounds how much outbound traffic ONE
	// anonymous request can cause.
	//
	// The publicness check hits a third-party registry with a package name
	// the caller chose, and it ran once per batch — so a single request could
	// fire 500 sequential probes at npmjs.org or pypi.org with names nobody
	// has ever published, since only an uncached name reaches the network.
	// That points this server at someone else, and it is the kind of thing
	// that gets a host blocked. Adding accounts is not the answer here
	// (goal.md §8.6); bounding the fan-out is.
	//
	// Batches naming a package already resolved this request cost nothing,
	// so an honest client sending many batches about a few packages is
	// unaffected. Past the cap the remaining NEW names are rejected as
	// unknown, which is the existing safe default rather than a new failure.
	maxRegistryLookupsPerRequest = 20
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
	// One publicness answer per distinct package per request, and a hard cap
	// on how many NEW names can reach a third-party registry.
	seenPublicness := map[string]string{}
	lookups := 0
	for i, b := range req.Batches {
		if err := serverstore.ValidateBatch(b); err != nil {
			rejected = append(rejected, serverstore.RejectedBatch{Index: i, Reason: err.Error()})
			continue
		}
		if !a.trustMode() {
			p, _ := domain.ParsePURL(b.Package) // parse checked by ValidateBatch
			status := scanner.PublicnessUnknown
			key := p.String()
			switch {
			case a.d.Checker == nil:
			case seenPublicness[key] != "":
				// Already resolved in this request: free, and the common
				// case, since batches cluster on a handful of packages.
				status = seenPublicness[key]
			case lookups < maxRegistryLookupsPerRequest:
				lookups++
				status = a.d.Checker.Check(r.Context(), p)
				seenPublicness[key] = status
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
