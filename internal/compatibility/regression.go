package compatibility

import (
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Regression thresholds (goal.md §10.3): version V shows failRate ≥ 0.25
// while V-1 shows passRate ≥ 0.9 in the SAME environment bucket, and both
// buckets carry at least 5 observations.
const (
	regressionMinFailRate     = 0.25
	regressionMinPrevPassRate = 0.9
	regressionMinObservations = 5
)

// RegressionCandidate is a flagged — never confirmed — version regression.
// It stays a candidate: uncertain causes are hypotheses, not verdicts
// (goal.md §3.6).
type RegressionCandidate struct {
	Package         string `json:"package"`         // purl of the suspect version
	PreviousPackage string `json:"previousPackage"` // purl of V-1
	// CaseID is present for receipt-established candidates. It makes the
	// comparison auditable: both sides ran the same immutable problem rather
	// than merely mentioning the same symbol.
	CaseID string `json:"caseId,omitempty"`
	Symbol string `json:"symbol,omitempty"`
	// Receipt-established boundaries retain every comparison dimension that
	// made the two executions comparable. Consumers must not read an adapter-
	// specific failure as a package-wide verdict.
	VerifierAdapter   string                   `json:"verifierAdapter,omitempty"`
	SandboxCapability domain.SandboxCapability `json:"sandboxCapability,omitempty"`
	CompanionPackages []string                 `json:"companionPackages,omitempty"`
	HarnessHash       string                   `json:"harnessHash,omitempty"`
	// Stage is the stage the comparison was made at. Observation candidates
	// use a PROJECT_* stage; exact receipt boundaries use CONTRACT. A
	// regression only means something within one stage.
	Stage                string  `json:"stage,omitempty"`
	ContextLabel         string  `json:"contextLabel"`
	EnvBucketHash        string  `json:"envBucketHash"`
	FailRate             float64 `json:"failRate"`
	PreviousPassRate     float64 `json:"previousPassRate"`
	Observations         int64   `json:"observations"`
	PreviousObservations int64   `json:"previousObservations"`
}

// DetectRegressions applies the §10.3 rule per shared environment bucket
// between version V (cur) and version V-1 (prev) of the same package+symbol.
func DetectRegressions(curPURL, prevPURL, symbol string,
	cur, prev []serverstore.EvidenceRow) []RegressionCandidate {

	curStats := bucketPassFail(cur)
	prevStats := bucketPassFail(prev)

	keys := make([]stageBucketKey, 0, len(curStats))
	for k := range curStats {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ContextLabel != keys[j].ContextLabel {
			return keys[i].ContextLabel < keys[j].ContextLabel
		}
		if keys[i].EnvHash != keys[j].EnvHash {
			return keys[i].EnvHash < keys[j].EnvHash
		}
		return keys[i].Stage < keys[j].Stage
	})

	var out []RegressionCandidate
	for _, k := range keys {
		c := curStats[k]
		p, ok := prevStats[k]
		if !ok {
			// §10.3 compares the SAME env bucket, and the same STAGE. The
			// two used to be pooled: every observation stage went into one
			// tally, so "V-1 passed" could be satisfied entirely by USED
			// rows -- a symbol appearing in source, which is recorded as a
			// pass and essentially never fails -- while V's failures came
			// from PROJECT_COMPILE. That reported "1.11.0 compiled cleanly
			// in this environment" about a version nothing ever compiled
			// there, and it was biased toward doing so, because the
			// always-passing stage inflated exactly the half of the rule
			// that gates the badge.
			continue
		}
		cTotal, pTotal := c.Pass+c.Fail, p.Pass+p.Fail
		if cTotal < regressionMinObservations || pTotal < regressionMinObservations {
			continue
		}
		failRate := float64(c.Fail) / float64(cTotal)
		prevPass := float64(p.Pass) / float64(pTotal)
		if failRate >= regressionMinFailRate && prevPass >= regressionMinPrevPassRate {
			out = append(out, RegressionCandidate{
				Package:              curPURL,
				PreviousPackage:      prevPURL,
				Symbol:               symbol,
				Stage:                k.Stage,
				ContextLabel:         k.ContextLabel,
				EnvBucketHash:        k.EnvHash,
				FailRate:             failRate,
				PreviousPassRate:     prevPass,
				Observations:         cTotal,
				PreviousObservations: pTotal,
			})
		}
	}
	return out
}

// stageBucketKey is one environment bucket at one observation stage.
// Comparing V against V-1 is only meaningful within a single stage: a
// typecheck pass says nothing about whether the thing ran.
type stageBucketKey struct {
	bucketKey
	Stage string
}

// bucketPassFail tallies observation pass/fail per env bucket AND stage.
func bucketPassFail(rows []serverstore.EvidenceRow) map[stageBucketKey]StageCount {
	out := map[stageBucketKey]StageCount{}
	for _, row := range rows {
		if !isObservationStage(row.Stage) {
			continue
		}
		env, ok := parseEnv(row.EnvJSON)
		if !ok {
			continue
		}
		bk, _ := bucketOf(env)
		k := stageBucketKey{bucketKey: bk, Stage: row.Stage}
		sc := out[k]
		if row.Result == string(domain.ResultPass) {
			sc.Pass += row.ObservationCount
		} else {
			sc.Fail += row.ObservationCount
		}
		out[k] = sc
	}
	return out
}
