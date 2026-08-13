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
	Package              string  `json:"package"`         // purl of the suspect version
	PreviousPackage      string  `json:"previousPackage"` // purl of V-1
	Symbol               string  `json:"symbol,omitempty"`
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

	keys := make([]bucketKey, 0, len(curStats))
	for k := range curStats {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ContextLabel != keys[j].ContextLabel {
			return keys[i].ContextLabel < keys[j].ContextLabel
		}
		return keys[i].EnvHash < keys[j].EnvHash
	})

	var out []RegressionCandidate
	for _, k := range keys {
		c := curStats[k]
		p, ok := prevStats[k]
		if !ok {
			continue // §10.3 compares the SAME env bucket
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

// bucketPassFail tallies observation-stage pass/fail per env bucket.
func bucketPassFail(rows []serverstore.EvidenceRow) map[bucketKey]StageCount {
	out := map[bucketKey]StageCount{}
	for _, row := range rows {
		if !isObservationStage(row.Stage) {
			continue
		}
		env, ok := parseEnv(row.EnvJSON)
		if !ok {
			continue
		}
		k, _ := bucketOf(env)
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
