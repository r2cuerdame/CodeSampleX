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
	publicness := func(p domain.PURL) string {
		// A fixed public target is public, and no registry can say so.
		//
		// The checker exists to ask npm, PyPI, crates.io and the rest.
		// engine/unreal lives on none of them, so the answer was UNKNOWN and
		// UNKNOWN is refused -- which meant every observation an Unreal
		// project could produce was rejected on arrival, retried on the next
		// sync, and rejected again, forever. domain.IsWantedTarget is
		// already the publicness boundary for coordinates that do not live
		// on a registry; it simply was not consulted here.
		if status := publicTargetPublicness(p); status == scanner.PublicnessPublic {
			return status
		}
		if a.d.Checker == nil {
			return scanner.PublicnessUnknown
		}
		key := p.String()
		if status := seenPublicness[key]; status != "" {
			// Already resolved in this request: free, and the common
			// case, since batches cluster on a handful of packages.
			return status
		}
		// An answer the server already holds costs no outbound request, so it
		// must not be charged to a cap that exists to bound outbound requests.
		// Charging it meant a request about packages this server knows well
		// spent the entire budget without contacting anyone, and every package
		// past the twentieth was refused as publicness-unknown. That refusal
		// is retryable by design, so those batches came back every sync and
		// were refused the same way — 226 packages in production had never
		// been checked once, the oldest first seen two weeks earlier, while a
		// farm daemon refused 826, 890, 976, 916 and 920 batches on five
		// consecutive cycles with the number never falling (#106).
		if cached, ok := a.d.Checker.(CachedPublicnessChecker); ok {
			if status, hit := cached.CachedPublicness(r.Context(), p); hit {
				seenPublicness[key] = status
				return status
			}
		}
		if lookups >= maxRegistryLookupsPerRequest {
			return scanner.PublicnessUnknown
		}
		lookups++
		status := a.d.Checker.Check(r.Context(), p)
		seenPublicness[key] = status
		return status
	}
	for i, b := range req.Batches {
		if err := serverstore.ValidateBatch(b); err != nil {
			rejected = append(rejected, serverstore.RejectedBatch{
				Index: i, Reason: err.Error(),
				Code: serverstore.RejectInvalidBatch, Terminal: true,
			})
			continue
		}
		if !a.trustMode() {
			p, _ := domain.ParsePURL(b.Package) // parse checked by ValidateBatch
			if status := publicness(p); status != scanner.PublicnessPublic {
				// UNKNOWN is treated as private — the safe default (§25.E) —
				// but it is not the same ANSWER as private, and the client
				// has to be able to tell them apart.
				//
				// PRIVATE is the registry's decision and will not change by
				// being asked again. UNKNOWN is this server declining to
				// store what it could not check: the per-request lookup
				// budget ran out, or no checker is configured. A client that
				// treated that as final would discard evidence about a public
				// package nobody had got around to confirming — and
				// production is exactly where that happens, because the
				// budget is per request and the backlog is thousands of
				// batches deep.
				rej := serverstore.RejectedBatch{
					Index: i, Reason: "package is not public (" + status + ")",
					Code: serverstore.RejectNotPublic, Terminal: true,
				}
				if status != scanner.PublicnessPrivate {
					rej.Reason = "package publicness not determined (" + status + ")"
					rej.Code, rej.Terminal = serverstore.RejectPublicnessUnknown, false
				}
				rejected = append(rejected, rej)
				continue
			}
			// A dependsOn child is a package name too, and the gate above
			// covers only the batch's own package. An edge with a private end
			// must not enter storage — it would be served on public pages —
			// but it is an auxiliary fact, so the child is dropped rather
			// than the batch: the observation itself is about a public
			// package and stays.
			b.DependsOn = publicDependsOn(b.DependsOn, publicness)
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

// publicTargetPublicness answers for the fixed public target vocabulary --
// engines and SDKs that have no registry -- and declines to answer for
// everything else.
//
// Deliberately narrow. The vocabulary is a closed list, which is what keeps
// this from becoming "any generic purl is public": an arbitrary generic name
// is exactly what would be sent to get an unchecked coordinate onto public
// pages, and it still resolves to UNKNOWN and is still refused.
func publicTargetPublicness(p domain.PURL) string {
	if domain.IsWantedTarget(p) {
		return scanner.PublicnessPublic
	}
	return scanner.PublicnessUnknown
}

// publicDependsOn keeps the edges whose child resolved as public. UNKNOWN is
// treated as private, the same safe default the package gate uses — including
// when the child was past the per-request registry lookup cap.
func publicDependsOn(edges []string, publicness func(domain.PURL) string) []string {
	var out []string
	for _, raw := range edges {
		child, err := domain.ParsePURL(raw) // parse checked by ValidateBatch
		if err != nil {
			continue
		}
		if publicness(child) == scanner.PublicnessPublic {
			out = append(out, raw)
		}
	}
	return out
}
