package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

const (
	// relayMinReporters is 1 on purpose, and the reasoning matters more than
	// the number.
	//
	// It was 3, to stop a lone reporter being dressed as a pattern. Measured
	// against the live corpus that silenced the feature completely: the
	// coordinates that carry observations carry them from ONE machine,
	// because adoption is small — and breaking that cold start is the entire
	// reason a miss relays anything at all.
	//
	// Withholding thin data is also the wrong instrument. Hiding a row
	// because it is thin is a judgement about sufficiency, and judgements are
	// what this payload does not make. The honest form is to relay the row
	// and SAY it came from one machine; every cell carries Reporters, and the
	// rendered text leads with it. A reader can weigh one machine against a
	// hundred far better than we can weigh it for them.
	//
	// Reporters reads UniquePeerBuckets, which ingest maintains as the PEAK
	// distinct reporters within a single epoch and never a sum across days,
	// so it cannot be inflated by one machine repeating.
	relayMinReporters = 1
	// relayMaxCells and relayMaxErrors bound the payload. A miss is the
	// network's most common outcome and this rides on every one of them.
	relayMaxCells  = 12
	relayMaxErrors = 8
)

// relayObservations returns what the network already recorded about the
// coordinate the caller asked about, when it has no verified sample to offer.
//
// It is a RELAY, not an answer. The project stands behind the samples it ran
// and the findings it detected; anonymous reports from other people's
// machines are neither, so they travel as counts and coordinates with their
// basis stated, and the grade stays NO_SAFE_MATCH. What changes is only that
// the miss stops being empty — the network routinely holds hundreds of
// recorded runs for a coordinate nobody has written a sample for, and a
// container farm can never close that gap: macOS has no container runtime,
// npm publishes no Windows image, and the tail of real installed versions is
// unbounded.
func (a *api) relayObservations(ctx context.Context, purls []domain.PURL, symbols []string) *domain.ObservedReports {
	if len(purls) == 0 {
		return nil
	}
	// The first named package is what the question is about. Relaying every
	// package in a dependency tree would bury it and multiply the read.
	purl := purls[0]
	snap, ok := a.relaySnapshot(ctx, purl.String(), symbols)
	if !ok {
		return nil
	}

	out := &domain.ObservedReports{
		PURL:  purl.String(),
		Basis: domain.ObservedBasis,
		Note:  domain.ObservedNote,
	}
	if len(symbols) == 1 {
		out.Symbol = symbols[0]
	}
	for _, row := range snap.Rows {
		if row.UniquePeerBuckets < relayMinReporters {
			continue
		}
		for stage, count := range row.ByStage {
			if !relayObservationStage(stage) || count.Pass+count.Fail == 0 {
				continue
			}
			out.Cells = append(out.Cells, domain.ObservedCell{
				Environment: relayEnvironment(row.EnvBucket),
				Stage:       stage,
				Pass:        count.Pass,
				Fail:        count.Fail,
				Reporters:   row.UniquePeerBuckets,
				LastSeen:    row.LastSeen,
			})
		}
	}
	if len(out.Cells) == 0 {
		return nil
	}
	// Most reported first, then most failing: a reader scanning this wants
	// the widely seen and the widely broken before anything else.
	sort.SliceStable(out.Cells, func(i, j int) bool {
		if out.Cells[i].Reporters != out.Cells[j].Reporters {
			return out.Cells[i].Reporters > out.Cells[j].Reporters
		}
		return out.Cells[i].Fail > out.Cells[j].Fail
	})
	if len(out.Cells) > relayMaxCells {
		out.Cells = out.Cells[:relayMaxCells]
	}

	for _, f := range snap.Failures {
		if f.ErrorCode == "" && f.Fingerprint == "" {
			continue
		}
		out.Errors = append(out.Errors, domain.ObservedError{
			Stage:       f.Stage,
			ErrorCode:   f.ErrorCode,
			Fingerprint: f.Fingerprint,
			Count:       f.Count,
			Environment: f.EnvSummary,
			Reporters:   f.Reporters,
			Projects:    f.Projects,
		})
	}
	// Ranked by how many machines saw it, not by how many times it happened.
	// A developer looping locally reports thousands of occurrences from one
	// machine, and ordering by Count puts them above a fleet-wide break.
	sort.SliceStable(out.Errors, func(i, j int) bool {
		if out.Errors[i].Reporters != out.Errors[j].Reporters {
			return out.Errors[i].Reporters > out.Errors[j].Reporters
		}
		return out.Errors[i].Count > out.Errors[j].Count
	})
	if len(out.Errors) > relayMaxErrors {
		out.Errors = out.Errors[:relayMaxErrors]
	}
	return out
}

// relaySnapshot reads the symbol-scoped document when the caller named one
// symbol, and the package-level one otherwise.
//
// It never reads both. The recorder writes a package-level observation AND
// one per detected symbol from the same build, carrying the same error
// fingerprint, so combining them multiplies the visible volume of a single
// build — in a payload whose entire value is an honest denominator.
func (a *api) relaySnapshot(ctx context.Context, purl string, symbols []string) (compatibility.Snapshot, bool) {
	symbol := ""
	if len(symbols) == 1 {
		symbol = strings.TrimSpace(symbols[0])
	}
	if snap, ok := a.readSnapshot(ctx, purl, symbol); ok {
		return snap, true
	}
	if symbol == "" {
		return compatibility.Snapshot{}, false
	}
	return a.readSnapshot(ctx, purl, "")
}

func (a *api) readSnapshot(ctx context.Context, purl, symbol string) (compatibility.Snapshot, bool) {
	raw, ok, err := a.d.Store.GetSnapshot(ctx, purl, symbol)
	if err != nil || !ok || raw == "" {
		return compatibility.Snapshot{}, false
	}
	var snap compatibility.Snapshot
	if json.Unmarshal([]byte(raw), &snap) != nil {
		return compatibility.Snapshot{}, false
	}
	return snap, true
}

// relayObservationStage keeps the relay to what developer machines reported.
// The CONTRACT key in a snapshot comes from signed receipts, which are the
// thing this project asserts; mixing them in would let a proof travel under
// the "observed" basis.
func relayObservationStage(stage string) bool {
	return stage == string(domain.StageUsed) || strings.HasPrefix(stage, "PROJECT_")
}

// relayEnvironment projects the recorded fingerprint onto the closed set of
// dimensions that may travel. The full record carries roughly twenty-five
// fields including distro, libc version, container runtime, virtualization,
// frameworks and CI; those against a stable anonymous id are a fingerprint,
// not a measurement.
func relayEnvironment(env domain.EnvironmentFingerprint) domain.ObservedEnvironment {
	b := env.Bucketed()
	return domain.ObservedEnvironment{
		OS:             b.OS,
		Arch:           b.Arch,
		Runtime:        b.Runtime,
		RuntimeVersion: b.RuntimeVersion,
		PackageManager: b.PackageManager,
		Libc:           b.Libc,
		Context:        b.ExecutionContext,
	}
}

// relayOnMiss attaches the relay to a miss, and only to a miss.
//
// v1 is a frozen byte shape that would discard the payload after paying for
// it, on the network's most common outcome.
func (a *api) relayOnMiss(r *http.Request, version int, resp *domain.SearchResponse,
	purls []domain.PURL, symbols []string) {
	if version < 2 || !resp.Miss {
		return
	}
	resp.Observed = a.relayObservations(r.Context(), purls, symbols)
}
