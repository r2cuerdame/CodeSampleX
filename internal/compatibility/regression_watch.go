package compatibility

import (
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Kinds of change a RegressionWatchCandidate reports.
const (
	// WatchKnownGoodNowFailing: every earlier measurement passed and every
	// later one failed. A known-good coordinate changed.
	WatchKnownGoodNowFailing = "KNOWN_GOOD_NOW_FAILING"
	// WatchKnownFailureNowPassing: every earlier measurement failed and
	// every later one passed. A known boundary was crossed from below.
	WatchKnownFailureNowPassing = "KNOWN_FAILURE_NOW_PASSING"
)

// RegressionWatchCandidate is one measured coordinate whose newer
// measurements contradict its earlier, unanimous verdict under identical
// comparison conditions. It is a request for revalidation, not a verdict.
//
// It exists because a coordinate measured both ways is deliberately silent
// everywhere else: it anchors no boundary and no upgrade path, since a claim
// in either direction would be one the evidence does not support. Silence is
// right for those products and wrong as history — the boundary that stood
// until yesterday, and the measurement that crossed it, would both vanish
// from every document the moment the second receipt landed. This candidate
// is where they stay visible: the earlier verdict with its count and the
// time it held through, the later verdict with its count and the time it
// began, and the pass below it that the old boundary rested on.
//
// Only a clean change in time qualifies. Interleaved verdicts have no older
// side to preserve, and a receipt with no recorded time cannot be ordered at
// all; both yield no candidate.
type RegressionWatchCandidate struct {
	Package string `json:"package"` // purl of the coordinate whose verdict changed
	CaseID  string `json:"caseId"`
	Symbol  string `json:"symbol,omitempty"`
	Stage   string `json:"stage"`
	// Kind names the direction of the change; see the Watch* constants.
	Kind string `json:"kind"`
	// The conditions every measurement on both sides was taken under.
	VerifierAdapter   string                   `json:"verifierAdapter"`
	SandboxCapability domain.SandboxCapability `json:"sandboxCapability"`
	CompanionPackages []string                 `json:"companionPackages,omitempty"`
	HarnessHash       string                   `json:"harnessHash"`
	ContextLabel      string                   `json:"contextLabel"`
	EnvBucketHash     string                   `json:"envBucketHash"`
	// The verdict that held first, how many measurements carry it, and the
	// time of the last one. This is the history a new receipt would
	// otherwise erase.
	KnownResult       string `json:"knownResult"`
	KnownObservations int64  `json:"knownObservations"`
	KnownThrough      string `json:"knownThrough"` // RFC3339
	// The verdict that contradicts it, how many measurements carry it, and
	// the time of the first one.
	LatestResult       string `json:"latestResult"`
	LatestObservations int64  `json:"latestObservations"`
	LatestSince        string `json:"latestSince"` // RFC3339
	// NearestLowerPassPackage is the closest measured release below this
	// one with an unambiguous PASS, when there is one. For a known failure
	// now passing it names the pass side of the boundary this crosses; for
	// a known-good coordinate now failing it is the last release below it
	// that the same conditions still show working. Empty means no measured
	// release below it passed, never that one does not exist.
	NearestLowerPassPackage string `json:"nearestLowerPassPackage,omitempty"`
}

// regressionWatchFromReceipts derives one candidate per measured coordinate
// whose verdict changed cleanly over time.
//
// It reads the same receiptVersionGroups that boundaries and upgrade paths
// do, so the coordinate it flags is exactly the one those two products fell
// silent on.
func regressionWatchFromReceipts(samples []sampleData) map[receiptTarget][]RegressionWatchCandidate {
	out := map[receiptTarget][]RegressionWatchCandidate{}

	for key, byVersion := range receiptVersionGroups(samples) {
		versions := sortedMeasuredVersions(byVersion)
		for i, version := range versions {
			v := byVersion[version]
			candidate, ok := watchCandidate(v)
			if !ok {
				continue
			}
			for j := i - 1; j >= 0; j-- {
				lower := byVersion[versions[j]]
				if result, _, ok := lower.unambiguous(); ok && result == string(domain.ResultPass) {
					candidate.NearestLowerPassPackage = lower.purl
					break
				}
			}
			candidate.CaseID, candidate.Symbol = key.caseID, key.symbol
			candidate.Stage = string(domain.StageContract)
			candidate.VerifierAdapter, candidate.SandboxCapability = key.verifierAdapter, key.sandboxCapability
			candidate.CompanionPackages, candidate.HarnessHash = companionPackages(key.companions), key.harnessHash
			candidate.ContextLabel, candidate.EnvBucketHash = key.contextLabel, key.envBucketHash
			target := receiptTarget{purl: v.purl, symbol: key.symbol}
			out[target] = append(out[target], candidate)
		}
	}

	for target := range out {
		sort.Slice(out[target], func(i, j int) bool {
			a, b := out[target][i], out[target][j]
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

// watchCandidate reads the two sides of one version's measurements and
// reports the change between them, when there is a clean one.
func watchCandidate(v *receiptVersionVerdict) (RegressionWatchCandidate, bool) {
	if v.pass == 0 || v.fail == 0 || v.undated {
		return RegressionWatchCandidate{}, false
	}
	c := RegressionWatchCandidate{Package: v.purl}
	switch {
	case v.lastPass.Before(v.firstFail):
		c.Kind = WatchKnownGoodNowFailing
		c.KnownResult, c.KnownObservations, c.KnownThrough = string(domain.ResultPass), v.pass, rfc3339(v.lastPass)
		c.LatestResult, c.LatestObservations, c.LatestSince = string(domain.ResultFail), v.fail, rfc3339(v.firstFail)
	case v.lastFail.Before(v.firstPass):
		c.Kind = WatchKnownFailureNowPassing
		c.KnownResult, c.KnownObservations, c.KnownThrough = string(domain.ResultFail), v.fail, rfc3339(v.lastFail)
		c.LatestResult, c.LatestObservations, c.LatestSince = string(domain.ResultPass), v.pass, rfc3339(v.firstPass)
	default:
		// Interleaved, or two verdicts at one instant: nothing is older.
		return RegressionWatchCandidate{}, false
	}
	return c, true
}

func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
