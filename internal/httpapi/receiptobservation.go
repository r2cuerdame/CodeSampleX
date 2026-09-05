package httpapi

import (
	"encoding/json"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Writing a sample and running its contract IS an execution, in an
// environment this network recorded rather than assumed. It was kept only as
// a verification, so a coordinate the farm had built itself showed a dash
// where its own runs belonged: 329 packages and 7,173 snapshot rows reading
// "never measured" about work we did.
//
// This does not merge the two classes into one number. The batches carry the
// peer that ran them and one project bucket per sample, so a reader sees one
// reporting peer and can tell whose machine it was — and the receipt is still
// a receipt. What changes is that the run is recorded at all.
//
// It reads a receipt the server already holds, so no worker changes and the
// existing rows can be backfilled from the same function.

// receiptStageToObservation maps what a contract run did to the stage names
// the aggregation counts as an observation.
//
// The cube counts USED and anything beginning with PROJECT_. A contract run
// resolves the package set, may compile it, loads it and executes the
// assertions — all of that happened in the recorded environment, and each has
// a project-scoped stage that says so.
var receiptStageToObservation = map[string]domain.Stage{
	"resolve":  domain.StageUsed,
	"compile":  domain.StageProjectCompile,
	"load":     domain.StageProjectLoad,
	"contract": domain.StageProjectTest,
}

type receiptBody struct {
	SchemaVersion    int                           `json:"schemaVersion"`
	Stages           map[string]string             `json:"stages"`
	ResolvedPackages []string                      `json:"resolvedPackages"`
	Environment      domain.EnvironmentFingerprint `json:"environment"`
}

// ObservationsFromReceipt turns one verification receipt into the usage
// observations it already proves.
//
// Exported because the receipts already stored need the same conversion, and
// backfilling them through a second implementation would be a second set of
// rules about what a receipt proves.
//
// A receipt that does not name the packages it resolved is attributed to no
// coordinate at all: inventing one would be worse than the dash it leaves.
// When sample manifest symbols are provided, passing contract executions are
// also attributed to those declared symbols at StageProjectTest with
// SymbolExact confidence so verified samples satisfy Observation >= 1.
func ObservationsFromReceipt(r serverstore.ReceiptRow, symbols ...string) []domain.ObservationBatch {
	var body receiptBody
	if json.Unmarshal([]byte(r.ReceiptJSON), &body) != nil {
		return nil
	}
	if len(body.ResolvedPackages) == 0 || r.PeerID == "" || r.SampleID == "" {
		return nil
	}
	env := body.Environment.Normalize()
	if env.Ecosystem == "" || env.OS == "" || env.Arch == "" {
		// The ingest path requires these, and a run we cannot place is one we
		// must not record.
		return nil
	}
	epoch := r.CreatedAt.UTC().Format("2006-01-02")

	var out []domain.ObservationBatch
	for idx, raw := range body.ResolvedPackages {
		purl, err := domain.ParsePURL(raw)
		if err != nil || !domain.ConcreteResolvedVersion(purl.Version) {
			continue
		}
		for name, stage := range receiptStageToObservation {
			result, ran := receiptStageResult(body.Stages[name])
			if !ran && name == "contract" && r.ContractResult != "" {
				result, ran = receiptStageResult(r.ContractResult)
			}
			if !ran {
				continue
			}
			batch := domain.ObservationBatch{
				SchemaVersion: 1,
				Epoch:         epoch,
				AnonID:        r.PeerID,
				// One contract run is one place. The sample id keeps the
				// project-bucket count honest: many receipts for one sample
				// are one project, not many.
				// The digest without its "sha256:" prefix, because a bucket may
				// be 64 bytes and the full id is 71. Carrying the prefix cost the
				// first backfill every one of its 9,883 observations: the store
				// refused them all and the run reported it as a bare count.
				ProjectBucket:    receiptProjectBucket(r.SampleID),
				Package:          purl.String(),
				Symbol:           "",
				SymbolConfidence: domain.SymbolUnknown,
				Environment:      env,
				Stage:            stage,
				Result:           result,
				ObservationCount: 1,
				// The sample declares these packages; they are not something
				// it received through somebody else's tree.
				Direct: true,
			}
			if stage == domain.StageUsed {
				if len(body.ResolvedPackages) == 1 {
					batch.DependsOnNone = true
				} else if idx == 0 {
					kids := make([]string, 0, len(body.ResolvedPackages)-1)
					for _, k := range body.ResolvedPackages[1:] {
						if kp, err := domain.ParsePURL(k); err == nil && domain.ConcreteResolvedVersion(kp.Version) {
							kids = append(kids, kp.String())
						}
					}
					if len(kids) > domain.MaxDependsOnPerBatch {
						kids = kids[:domain.MaxDependsOnPerBatch]
					}
					batch.DependsOn = kids
				}
			}
			out = append(out, batch)

			if stage == domain.StageProjectTest && idx == 0 && len(symbols) > 0 {
				seen := make(map[string]bool, len(symbols))
				for _, sym := range symbols {
					sym = strings.TrimSpace(sym)
					if sym == "" || seen[sym] {
						continue
					}
					seen[sym] = true
					out = append(out, domain.ObservationBatch{
						SchemaVersion:    1,
						Epoch:            epoch,
						AnonID:           r.PeerID,
						ProjectBucket:    receiptProjectBucket(r.SampleID),
						Package:          purl.String(),
						Symbol:           sym,
						SymbolConfidence: domain.SymbolExact,
						Environment:      env,
						Stage:            stage,
						Result:           result,
						ObservationCount: 1,
						Direct:           true,
					})
				}
			}
		}
	}
	return out
}

// receiptStageResult reads one stage verdict, and reports whether the stage
// ran at all. SKIPPED did not happen and must not be recorded as though it
// had.
func receiptStageResult(verdict string) (domain.Result, bool) {
	switch strings.ToUpper(strings.TrimSpace(verdict)) {
	case "PASS":
		return domain.ResultPass, true
	case "FAIL":
		return domain.ResultFail, true
	default:
		return "", false
	}
}

// receiptProjectBucket is the sample id as a bucket the store will accept.
//
// A bucket may be 64 bytes; "sha256:" plus 64 hex is 71. The digest alone is
// exactly 64 and identifies the sample just as well, so nothing about "many
// receipts for one sample are one project" changes.
func receiptProjectBucket(sampleID string) string {
	return domain.SampleProjectBucket(sampleID)
}
