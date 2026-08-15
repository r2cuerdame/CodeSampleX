package search

import (
	"strconv"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Score-fusion weights (C7 §11.3 order). Exact package/symbol/error tokens
// dominate lexical similarity by construction: environment fit and
// verification strength then gate and rerank the fused base.
const (
	weightPackageExact      = 0.45 // step 1: exact purl (version included)
	weightPackageMajorMinor = 0.40 // step 1: same major.minor
	weightPackageMajor      = 0.28 // step 1: same major
	weightPackageMajorDiff  = 0.10 // requested package, other major
	weightSymbol            = 0.20 // step 2: symbol family match
	weightErrorFingerprint  = 0.60 // step 3: exact fingerprint hit
	weightErrorCode         = 0.30 // step 3: exact error-code hit
	weightFTS               = 0.30 // step 4: normalized BM25
	weightIntent            = 0.15 // step 5: token-overlap intent similarity
	// weightNamedSubject: the question names the candidate's package or one
	// of its symbols. Naming the library is the strongest thing a question
	// can do short of pinning a version, and it was worth nothing: the
	// identifier only opened the relevance gate, then the score was decided
	// by lexical overlap alone.
	//
	// "parse command line flags in rust with clap" ranked an npm commander
	// sample above the clap one, because BM25 loves "parse command line" and
	// nothing carried the two words that settle it.
	weightNamedSubject = 0.20

	// treeRelevanceFactor discounts a package match that came from the
	// caller's dependency tree rather than from the question.
	//
	// Without it an exact-version match scores 0.45, and textual relevance
	// can reach 0.45 in total — so any package sitting in the lockfile
	// outranked every sample the question was actually about. Asked
	// "validate a map with Ecto.Changeset without a Repo" from a Go
	// checkout, the engine answered with the google/uuid sample, graded
	// EXACT, because uuid was a dependency and its symbol uuid.Validate
	// tokenizes to the word validate.
	//
	// Having the package is real evidence — it is why the same question
	// from an empty directory and from a project that uses the library
	// should not return identical rankings — but it is evidence of "likely
	// relevant", never of "this is what was asked".
	treeRelevanceFactor = 0.4

	// missThreshold: best fused score below this is NO_SAFE_MATCH.
	missThreshold = 0.25
	// elevatedFailureMinCount is the minimum cluster size that demotes a
	// result in the matching requester environment.
	elevatedFailureMinCount = 5
)

// pkgRel orders package-version relations from worst to best fit.
type pkgRel int

const (
	relUnspecified pkgRel = iota // request named no packages
	relNone                      // request packages share nothing with the candidate
	relMajorDiff                 // same package, different major
	// relPackageOnly is "the same package, version not established". It sits
	// above a known major difference and below every claim about a version,
	// because it asserts less than any of them.
	//
	// It exists for candidates that came from a shard old enough not to carry
	// the sample's declared packages. Grading those off the shard key was
	// reporting a version the sample was never verified against as an exact
	// match, so the honest ceiling is the package name.
	relPackageOnly
	relMajor        // same major, different minor
	relMajorMinor   // same major.minor, different patch
	relExactVersion // identical version
)

// packageRelation finds the best (request package, candidate package) pair.
func packageRelation(reqPkgs, candPkgs []domain.PURL) (pkgRel, domain.PURL, domain.PURL) {
	var reqP, samP domain.PURL
	if len(candPkgs) > 0 {
		samP = candPkgs[0]
	}
	if len(reqPkgs) == 0 {
		return relUnspecified, reqP, samP
	}
	reqP = reqPkgs[0]
	best := relNone
	found := false
	for _, rp := range reqPkgs {
		for _, cp := range candPkgs {
			if rp.Ecosystem != cp.Ecosystem || !equalFoldName(rp.Name, cp.Name) {
				continue
			}
			var r pkgRel
			switch {
			case rp.Version == cp.Version:
				r = relExactVersion
			case rp.MajorMinor() == cp.MajorMinor():
				r = relMajorMinor
			// BreakingBucket, not Major: semver makes a 0.x minor bump as
			// breaking as a major one, so 0.6 and 0.8 are not "the same
			// major, different minor" — they are a different line entirely.
			case rp.BreakingBucket() == cp.BreakingBucket():
				r = relMajor
			default:
				r = relMajorDiff
			}
			// The WORST shared pair is the one graded, not the friendliest.
			//
			// Keeping the best meant a caller naming
			// [react@19.2.0, react-dom@19.2.0] against a sample declaring
			// [react@19.2.0, react-dom@18.3.1] got MATCH: EXACT on react
			// while the react-dom major gap — the thing that would actually
			// break their build — went unmentioned in the delta. The
			// server-side search already grades the widest gap for exactly
			// this reason; this is the same input getting two answers.
			//
			// An honest summary of fit is the worst mismatch among the
			// packages both sides share.
			if !found || r < best {
				best, reqP, samP, found = r, rp, cp, true
			}
		}
	}
	if !found {
		return relNone, reqP, samP
	}
	return best, reqP, samP
}

func relWeight(r pkgRel) float64 {
	switch r {
	case relExactVersion:
		return weightPackageExact
	case relMajorMinor:
		return weightPackageMajorMinor
	case relMajor:
		return weightPackageMajor
	case relPackageOnly:
		return weightPackageMajor
	case relMajorDiff:
		return weightPackageMajorDiff
	}
	return 0
}

// envFit is the step-6 environment gate as a multiplier. Context/engine
// mismatch penalizes harder than an ordinary adaptable difference, and
// browserMajor distance scores like minor-version distance
// (docs/execution-context.md §5).
func envFit(grade domain.MatchGrade, cd contextDelta) float64 {
	switch grade {
	case domain.GradeExact:
		return 1
	case domain.GradeCompatible:
		return 0.9
	case domain.GradeAdaptationRequired:
		f := 0.7
		if cd.mismatch {
			f = 0.55
		}
		f -= 0.02 * float64(cd.majorDistance)
		if f < 0.4 {
			f = 0.4
		}
		return f
	default: // REFERENCE_ONLY
		return 0.35
	}
}

// verificationLevel maps sample status + receipts to a verification level
// number (L0..L5). Receipts can only raise the level they prove: one
// contract-PASS receipt is L3, contract-PASS receipts from two independent
// peers are L4 (C13 lifecycle).
func verificationLevel(status string, contractStages map[string]string, receipts []domain.VerificationReceipt) int {
	lvl := 0
	switch status {
	case "STABLE", "MATRIX_PASS":
		lvl = 5
	case "CROSS_PASS":
		lvl = 4
	case "LOCAL_PASS":
		lvl = 3
	}
	// An unnamed receipt is not a peer. PeerID "" was being counted as a
	// distinct key, so one anonymous receipt beside one real one reached
	// L4 — "contract-PASS receipts from two INDEPENDENT peers" — and took
	// the ×3 strength multiplier with it. Independence is the one thing a
	// verification level asserts that a single publisher cannot manufacture,
	// and an empty string was manufacturing it.
	peers := map[string]bool{}
	passes := 0
	for _, r := range receipts {
		if res, ok := contractResult(r.Stages); ok && res == domain.ResultPass {
			passes++
			if r.PeerID != "" {
				peers[r.PeerID] = true
			}
		}
	}
	switch {
	case len(peers) >= 2 && lvl < 4:
		lvl = 4
	case passes >= 1 && lvl < 3:
		lvl = 3
	}
	if contractStages["contract"] == "PASS" && lvl < 3 {
		lvl = 3
	}
	return lvl
}

// strengthBoost is the step-7 rerank: L3+ receipts boost ×2, L4+ ×3 (C7).
func strengthBoost(level int) float64 {
	switch {
	case level >= 4:
		return 3
	case level == 3:
		return 2
	}
	return 1
}

// intDistance is |a-b| over numeric version buckets; non-numeric input
// counts as distance 1 when unequal.
func intDistance(a, b string) int {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr != nil || berr != nil {
		if a == b {
			return 0
		}
		return 1
	}
	if ai > bi {
		return ai - bi
	}
	return bi - ai
}
