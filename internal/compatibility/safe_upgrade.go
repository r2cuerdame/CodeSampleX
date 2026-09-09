package compatibility

import (
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// SafeUpgradeCandidate is the highest version reachable from one measured
// coordinate along an unbroken run of measured PASSes, under the exact
// conditions that produced those measurements.
//
// It summarizes evidence; it does not promise safety. Four limits are
// deliberate, and each one is a way the obvious version of this feature would
// have claimed more than it measured:
//
//   - Only measured versions appear. A release that exists on the registry but
//     was never verified here is not on the path and is not claimed either
//     way, so MeasuredPath is the list of versions the claim rests on rather
//     than a range spanning its endpoints.
//   - The run stops at the first version without an unambiguous verdict. A
//     measured FAIL ends it and is reported as BlockedByVersion; a version
//     measured both ways ends it silently, because "unproven" and "broken"
//     are different answers and only one of them is a boundary.
//   - Nothing is claimed above the highest measured version. A coordinate with
//     no measurement above it yields no candidate at all, which is not the
//     same statement as "you are already on the newest safe release".
//   - The path holds only within the conditions on this struct. The same two
//     versions under another adapter, sandbox, environment bucket or resolved
//     dependency set are a different question that these receipts did not ask.
type SafeUpgradeCandidate struct {
	Package       string `json:"package"`       // purl of the coordinate the reader is on
	TargetPackage string `json:"targetPackage"` // purl of the highest evidence-backed version
	CaseID        string `json:"caseId"`
	Symbol        string `json:"symbol,omitempty"`
	// CurrentResult separates an upgrade from a working coordinate from an
	// escape off a failing one. Both are useful and they are not the same
	// suggestion, so they must not read alike.
	CurrentResult string `json:"currentResult"`
	Stage         string `json:"stage"`
	// The conditions every step of the path was measured under. A consumer
	// must not widen an adapter-and-environment-specific path into a
	// package-wide recommendation.
	VerifierAdapter   string                   `json:"verifierAdapter"`
	SandboxCapability domain.SandboxCapability `json:"sandboxCapability"`
	CompanionPackages []string                 `json:"companionPackages,omitempty"`
	HarnessHash       string                   `json:"harnessHash"`
	ContextLabel      string                   `json:"contextLabel"`
	EnvBucketHash     string                   `json:"envBucketHash"`
	// MeasuredPath is every version strictly above the current coordinate up
	// to and including the target, ascending. Each one passed here.
	MeasuredPath []string `json:"measuredPath"`
	// BlockedByVersion is the measured failure directly above the target, when
	// one is what ends the path. Empty means nothing above the target carries
	// an unambiguous verdict — never that the path continues past it.
	BlockedByVersion    string `json:"blockedByVersion,omitempty"`
	CurrentObservations int64  `json:"currentObservations"`
	TargetObservations  int64  `json:"targetObservations"`
}

// safeUpgradesFromReceipts derives one candidate per measured coordinate that
// has somewhere evidence-backed to go.
//
// It reads the same receiptVersionGroups that regression boundaries do, so the
// version a path recommends and the version a boundary condemns are always
// drawn from one set of comparable measurements.
func safeUpgradesFromReceipts(samples []sampleData) map[receiptTarget][]SafeUpgradeCandidate {
	out := map[receiptTarget][]SafeUpgradeCandidate{}

	for key, byVersion := range receiptVersionGroups(samples) {
		versions := sortedMeasuredVersions(byVersion)
		for i, version := range versions {
			current := byVersion[version]
			currentResult, currentCount, ok := current.unambiguous()
			if !ok {
				continue
			}

			// Walk up while every measured version passed. The first version
			// that did not ends the path, whatever the reason: a measured
			// failure and an unproven verdict both stop it here, and only the
			// failure is reported below as having stopped it.
			last, targetCount := i, int64(0)
			for j := i + 1; j < len(versions); j++ {
				result, count, ok := byVersion[versions[j]].unambiguous()
				if !ok || result != string(domain.ResultPass) {
					break
				}
				last, targetCount = j, count
			}
			if last == i {
				continue // nothing measured above it passed
			}

			blockedBy := ""
			if last+1 < len(versions) {
				if result, _, ok := byVersion[versions[last+1]].unambiguous(); ok &&
					result == string(domain.ResultFail) {
					blockedBy = versions[last+1]
				}
			}

			target := receiptTarget{purl: current.purl, symbol: key.symbol}
			out[target] = append(out[target], SafeUpgradeCandidate{
				Package: current.purl, TargetPackage: byVersion[versions[last]].purl,
				CaseID: key.caseID, Symbol: key.symbol,
				CurrentResult: currentResult, Stage: string(domain.StageContract),
				VerifierAdapter: key.verifierAdapter, SandboxCapability: key.sandboxCapability,
				CompanionPackages: companionPackages(key.companions),
				HarnessHash:       key.harnessHash, ContextLabel: key.contextLabel,
				EnvBucketHash:       key.envBucketHash,
				MeasuredPath:        append([]string(nil), versions[i+1:last+1]...),
				BlockedByVersion:    blockedBy,
				CurrentObservations: currentCount, TargetObservations: targetCount,
			})
		}
	}

	for target := range out {
		sort.Slice(out[target], func(i, j int) bool {
			a, b := out[target][i], out[target][j]
			if a.TargetPackage != b.TargetPackage {
				return domain.CompareVersions(a.TargetPackage, b.TargetPackage) < 0
			}
			if a.CaseID != b.CaseID {
				return a.CaseID < b.CaseID
			}
			if a.EnvBucketHash != b.EnvBucketHash {
				return a.EnvBucketHash < b.EnvBucketHash
			}
			if a.VerifierAdapter != b.VerifierAdapter {
				return a.VerifierAdapter < b.VerifierAdapter
			}
			if a.SandboxCapability != b.SandboxCapability {
				return a.SandboxCapability < b.SandboxCapability
			}
			if a.HarnessHash != b.HarnessHash {
				return a.HarnessHash < b.HarnessHash
			}
			return strings.Join(a.CompanionPackages, "\x00") < strings.Join(b.CompanionPackages, "\x00")
		})
	}
	return out
}
