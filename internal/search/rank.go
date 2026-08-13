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
	relMajor                     // same major, different minor
	relMajorMinor                // same major.minor, different patch
	relExactVersion              // identical version
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
			case rp.Major() == cp.Major():
				r = relMajor
			default:
				r = relMajorDiff
			}
			if r > best {
				best, reqP, samP = r, rp, cp
			}
		}
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
	peers := map[string]bool{}
	passes := 0
	for _, r := range receipts {
		if res, ok := contractResult(r.Stages); ok && res == domain.ResultPass {
			passes++
			peers[r.PeerID] = true
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
