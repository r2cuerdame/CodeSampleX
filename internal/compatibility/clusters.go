package compatibility

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// BuildClusters groups FAIL evidence for one (ecosystem, packageName) across
// versions by (symbol, stage, error fingerprint) and attaches probabilistic
// failure hypotheses — never a definitive verdict (goal.md §3.6, §6.4).
// evidenceByVersion maps version → all evidence rows of that version
// (every symbol); regressions are the already-detected §10.3 candidates for
// this package.
func BuildClusters(ecosystem, packageName string,
	evidenceByVersion map[string][]serverstore.EvidenceRow,
	regressions []RegressionCandidate, now time.Time) []serverstore.ClusterRow {

	type ckey struct{ symbol, stage, fp string }
	type cluster struct {
		count    int64
		code     string
		failEnvs []domain.EnvironmentFingerprint
		versions map[string]bool
	}
	clusters := map[ckey]*cluster{}
	// passEnvs per (symbol, stage): where the same operation succeeded —
	// needed for the engine-concentration heuristic.
	passEnvs := map[[2]string][]domain.EnvironmentFingerprint{}

	for version, rows := range evidenceByVersion {
		for _, row := range rows {
			env, ok := parseEnv(row.EnvJSON)
			if !ok {
				continue
			}
			if row.Result == string(domain.ResultPass) {
				pk := [2]string{row.Symbol, row.Stage}
				passEnvs[pk] = append(passEnvs[pk], env)
				continue
			}
			if row.ErrorFingerprint == "" {
				continue // clusters key on the fingerprint
			}
			k := ckey{row.Symbol, row.Stage, row.ErrorFingerprint}
			c := clusters[k]
			if c == nil {
				c = &cluster{versions: map[string]bool{}}
				clusters[k] = c
			}
			c.count += row.ObservationCount
			if c.code == "" {
				c.code = row.ErrorCode
			}
			c.failEnvs = append(c.failEnvs, env)
			c.versions[version] = true
		}
	}

	// A regression candidate is a claim about ONE version pair at one
	// stage: "1.12.0 fails where 1.11.0 passed". Keying the badge by symbol
	// alone spread that claim over every failure cluster sharing the
	// symbol, so a cluster whose only version was 0.9.0 rendered
	// "▲ regression candidate  0.9.0" -- a definitive-sounding causal badge
	// on a version the comparison never looked at.
	//
	// It now needs the symbol, the stage, AND the suspect version.
	type regKey struct{ symbol, stage, version string }
	regressed := map[regKey]bool{}
	for _, r := range regressions {
		p, err := domain.ParsePURL(r.Package)
		if err != nil {
			continue
		}
		regressed[regKey{r.Symbol, r.Stage, p.Version}] = true
	}
	// isRegressed reports whether this cluster is the one the candidate is
	// about: same symbol and stage, and the suspect version among its own.
	isRegressed := func(symbol, stage string, versions map[string]bool) bool {
		for v := range versions {
			if regressed[regKey{symbol, stage, v}] {
				return true
			}
		}
		return false
	}

	keys := make([]ckey, 0, len(clusters))
	for k := range clusters {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].symbol != keys[j].symbol {
			return keys[i].symbol < keys[j].symbol
		}
		if keys[i].stage != keys[j].stage {
			return keys[i].stage < keys[j].stage
		}
		return keys[i].fp < keys[j].fp
	})

	out := make([]serverstore.ClusterRow, 0, len(keys))
	for _, k := range keys {
		c := clusters[k]
		hyps := Hypotheses(c.code, c.failEnvs, passEnvs[[2]string{k.symbol, k.stage}])
		versions := make([]string, 0, len(c.versions))
		for v := range c.versions {
			versions = append(versions, v)
		}
		versions = sortedVersions(versions)

		row := serverstore.ClusterRow{
			Ecosystem:           ecosystem,
			PackageName:         packageName,
			Symbol:              k.symbol,
			Stage:               k.stage,
			ErrorFingerprint:    k.fp,
			ErrorCode:           c.code,
			ObservationCount:    c.count,
			RegressionCandidate: isRegressed(k.symbol, k.stage, c.versions),
			LastSeen:            now,
		}
		if summary := envSummary(c.failEnvs); summary != nil {
			row.EnvSummaryJSON = string(domain.MustCanonicalJSON(summary))
		}
		row.HypothesesJSON = string(domain.MustCanonicalJSON(hyps))
		if b, err := json.Marshal(versions); err == nil {
			row.VersionsJSON = string(b)
		}
		out = append(out, row)
	}
	return out
}

// Hypotheses derives a probabilistic failure-domain distribution for one
// cluster. The result is ALWAYS a distribution, never a single confirmed
// cause: with no signal it is {UNKNOWN: 1.0}.
func Hypotheses(errorCode string, failEnvs, passEnvs []domain.EnvironmentFingerprint) []domain.FailureHypothesis {
	if isESMCode(errorCode) {
		// Module-system mismatches are overwhelmingly configuration issues
		// (package.json "type", tsconfig module), sometimes runtime version,
		// rarely a library regression.
		return []domain.FailureHypothesis{
			{Domain: domain.FailConfiguration, Confidence: 0.72},
			{Domain: domain.FailRuntime, Confidence: 0.21},
			{Domain: domain.FailLibraryRegression, Confidence: 0.07},
		}
	}
	if engine, ok := concentratedEngine(failEnvs, passEnvs); ok && engine != "" {
		// Every failing env shares one engine while the same operation
		// passes elsewhere — raise ENGINE/BROWSER confidence, keep a
		// library-regression tail (docs/execution-context.md §4).
		return []domain.FailureHypothesis{
			{Domain: domain.FailEngine, Confidence: 0.6},
			{Domain: domain.FailBrowser, Confidence: 0.25},
			{Domain: domain.FailLibraryRegression, Confidence: 0.15},
		}
	}
	return []domain.FailureHypothesis{{Domain: domain.FailUnknown, Confidence: 1.0}}
}

// isESMCode recognizes module-system error codes (ERR_REQUIRE_ESM and
// friends).
func isESMCode(code string) bool {
	if code == "" {
		return false
	}
	u := strings.ToUpper(code)
	return u == "ERR_REQUIRE_ESM" || strings.Contains(u, "ESM") ||
		u == "ERR_MODULE_NOT_FOUND" || u == "ERR_UNSUPPORTED_DIR_IMPORT"
}

// concentratedEngine reports the single engine shared by ALL failing envs,
// provided at least one passing env runs a different engine.
func concentratedEngine(failEnvs, passEnvs []domain.EnvironmentFingerprint) (string, bool) {
	if len(failEnvs) == 0 {
		return "", false
	}
	engine := failEnvs[0].Engine
	if engine == "" {
		return "", false
	}
	for _, e := range failEnvs[1:] {
		if e.Engine != engine {
			return "", false
		}
	}
	for _, e := range passEnvs {
		if e.Engine != "" && e.Engine != engine {
			return engine, true
		}
	}
	return "", false
}
