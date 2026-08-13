package compatibility

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// ReceiptInfo is the slice of a verification receipt the aggregation
// pipeline needs: who verified, where, and what the contract said.
type ReceiptInfo struct {
	PeerID         string
	Env            domain.EnvironmentFingerprint
	ContractResult string // PASS | FAIL | SKIPPED | ""
	Stages         map[string]string
	CreatedAt      time.Time
}

// ParseReceiptRow extracts ReceiptInfo from a stored receipt row.
func ParseReceiptRow(r serverstore.ReceiptRow) (ReceiptInfo, bool) {
	var rec domain.VerificationReceipt
	if err := json.Unmarshal([]byte(r.ReceiptJSON), &rec); err != nil {
		return ReceiptInfo{}, false
	}
	return ReceiptInfo{
		PeerID:         r.PeerID,
		Env:            rec.Environment.Normalize(),
		ContractResult: r.ContractResult,
		Stages:         rec.Stages,
		CreatedAt:      r.CreatedAt,
	}, true
}

// StageCount is the pass/fail tally of one stage.
type StageCount struct {
	Pass int64 `json:"pass"`
	Fail int64 `json:"fail"`
}

// parseEnv decodes an evidence row's env JSON. A row with unparsable env is
// skipped by callers rather than guessed at.
func parseEnv(envJSON string) (domain.EnvironmentFingerprint, bool) {
	var env domain.EnvironmentFingerprint
	if err := json.Unmarshal([]byte(envJSON), &env); err != nil {
		return domain.EnvironmentFingerprint{}, false
	}
	return env.Normalize(), true
}

// bucketKey identifies one snapshot/aggregation row group: the execution
// context label is the LEADING dimension (docs/execution-context.md §5),
// then the bucketed environment hash.
type bucketKey struct {
	ContextLabel string
	EnvHash      string
}

func bucketOf(env domain.EnvironmentFingerprint) (bucketKey, domain.EnvironmentFingerprint) {
	b := env.Normalize().Bucketed()
	return bucketKey{ContextLabel: b.ContextLabel(), EnvHash: b.Hash()}, b
}

// isObservationStage reports whether a stage is weak project-level
// observation evidence. Verification stages arrive only via receipts and
// are NEVER summed with these (goal.md §3.5).
func isObservationStage(stage string) bool {
	switch domain.Stage(stage) {
	case domain.StageUsed, domain.StageProjectTypecheck, domain.StageProjectCompile,
		domain.StageProjectTest, domain.StageProjectLoad, domain.StageProjectProcess:
		return true
	}
	return false
}

// envSummary reports the dimensions every environment in envs agrees on —
// the honest way to say "these failures share X" without claiming more.
func envSummary(envs []domain.EnvironmentFingerprint) map[string]string {
	if len(envs) == 0 {
		return nil
	}
	dims := func(e domain.EnvironmentFingerprint) map[string]string {
		e = e.Bucketed()
		m := map[string]string{}
		if e.OS != "" {
			m["os"] = e.OS
		}
		if e.Runtime != "" {
			v := e.Runtime
			if e.RuntimeVersion != "" {
				v += "@" + e.RuntimeVersion
			}
			m["runtime"] = v
		}
		if e.ModuleSystem != "" {
			m["moduleSystem"] = e.ModuleSystem
		}
		if e.ExecutionContext != "" {
			m["executionContext"] = e.ExecutionContext
		}
		if e.BrowserFamily != "" {
			m["browserFamily"] = e.BrowserFamily
		}
		if e.Engine != "" {
			m["engine"] = e.Engine
		}
		return m
	}
	common := dims(envs[0])
	for _, e := range envs[1:] {
		d := dims(e)
		for k, v := range common {
			if d[k] != v {
				delete(common, k)
			}
		}
	}
	if len(common) == 0 {
		return nil
	}
	return common
}

// sortedVersions returns versions in ascending numeric-ish order.
func sortedVersions(versions []string) []string {
	out := append([]string(nil), versions...)
	sort.Slice(out, func(i, j int) bool { return versionLess(out[i], out[j]) })
	return out
}

// PreviousVersion returns the greatest version strictly below current, for
// the §10.3 regression rule ("V-1").
func PreviousVersion(versions []string, current string) (string, bool) {
	best := ""
	found := false
	for _, v := range versions {
		if v == current || !versionLess(v, current) {
			continue
		}
		if !found || versionLess(best, v) {
			best, found = v, true
		}
	}
	return best, found
}

// versionLess compares dotted versions numerically segment by segment,
// ignoring a leading 'v' and any pre-release/build suffix.
func versionLess(a, b string) bool {
	as, bs := versionSegs(a), versionSegs(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			return av < bv
		}
	}
	return a < b
}

func versionSegs(v string) []int {
	if len(v) > 0 && (v[0] == 'v' || v[0] == 'V') {
		v = v[1:]
	}
	for i := 0; i < len(v); i++ {
		if v[i] == '-' || v[i] == '+' {
			v = v[:i]
			break
		}
	}
	var segs []int
	cur, has := 0, false
	for i := 0; i <= len(v); i++ {
		if i == len(v) || v[i] == '.' {
			if has {
				segs = append(segs, cur)
			}
			cur, has = 0, false
			continue
		}
		if v[i] >= '0' && v[i] <= '9' {
			cur = cur*10 + int(v[i]-'0')
			has = true
		}
	}
	return segs
}
