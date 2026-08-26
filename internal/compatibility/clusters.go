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
	type variant struct {
		env         domain.EnvironmentFingerprint
		count       int64
		first, last time.Time
	}
	type cluster struct {
		count       int64
		code        string
		termination string
		exitCode    *int
		signal      string
		timeout     int64
		summary     string
		quality     domain.EvidenceQuality
		toolchain   string
		stageProof  string
		gap         string
		outer       map[string]bool
		breakdown   map[string]int64
		failEnvs    []domain.EnvironmentFingerprint
		variants    map[string]*variant
		versions    map[string]bool
		first, last time.Time
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
			q := evidenceQuality(row)
			clusterFingerprint := row.ErrorFingerprint
			if q == domain.EvidenceMissing || q == domain.EvidenceLegacyIncomplete {
				// Historical hashes and empty-error hashes are provenance, not
				// failure identities. Collapse them into an explicit stage-level
				// Evidence gap instead of rendering N fake fingerprints.
				clusterFingerprint = ""
			}
			k := ckey{row.Symbol, row.Stage, clusterFingerprint}
			c := clusters[k]
			if c == nil {
				c = &cluster{versions: map[string]bool{}, variants: map[string]*variant{}, breakdown: map[string]int64{}, outer: map[string]bool{}}
				clusters[k] = c
			}
			c.count += row.ObservationCount
			c.breakdown[string(q)] += row.ObservationCount
			if c.quality == "" || q == domain.EvidenceLegacyIncomplete || (q == domain.EvidenceMissing && c.quality != domain.EvidenceLegacyIncomplete) {
				c.quality = q
			}
			if c.code == "" {
				c.code = row.ErrorCode
			}
			if c.termination == "" {
				c.termination, c.exitCode, c.signal, c.timeout = row.TerminationKind, row.ExitCode, row.Signal, row.TimeoutMillis
			}
			if c.summary == "" {
				c.summary = row.ErrorSummary
			}
			if c.toolchain == "" {
				c.toolchain, c.stageProof, c.gap = row.ActualToolchain, row.StageEvidence, row.FailureEvidenceGap
			}
			for _, command := range row.OuterCommands {
				if command != "" {
					c.outer[command] = true
				}
			}
			if len(row.OuterCommands) == 0 && row.OuterCommand != "" {
				c.outer[row.OuterCommand] = true
			}
			c.failEnvs = append(c.failEnvs, env)
			v := c.variants[row.EnvHash]
			if v == nil {
				v = &variant{env: env, first: row.FirstSeen, last: row.LastSeen}
				c.variants[row.EnvHash] = v
			}
			v.count += row.ObservationCount
			if v.first.IsZero() || (!row.FirstSeen.IsZero() && row.FirstSeen.Before(v.first)) {
				v.first = row.FirstSeen
			}
			if row.LastSeen.After(v.last) {
				v.last = row.LastSeen
			}
			if c.first.IsZero() || (!row.FirstSeen.IsZero() && row.FirstSeen.Before(c.first)) {
				c.first = row.FirstSeen
			}
			if row.LastSeen.After(c.last) {
				c.last = row.LastSeen
			}
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
		hyps := []domain.FailureHypothesis{}
		modernEvidence := c.quality == domain.EvidenceComplete || c.quality == domain.EvidencePartial
		if k.fp != "" && modernEvidence {
			hyps = Hypotheses(c.code, c.failEnvs, passEnvs[[2]string{k.symbol, k.stage}])
		}
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
			TerminationKind:     c.termination,
			ExitCode:            c.exitCode,
			Signal:              c.signal,
			TimeoutMillis:       c.timeout,
			ErrorSummary:        c.summary,
			EvidenceQuality:     string(c.quality),
			ActualToolchain:     c.toolchain,
			StageEvidence:       c.stageProof,
			FailureEvidenceGap:  c.gap,
			ObservationCount:    c.count,
			RegressionCandidate: isRegressed(k.symbol, k.stage, c.versions),
			DiagnosticCandidate: c.gap != "" || (!modernEvidence && c.count >= 2),
			FirstSeen:           c.first,
			LastSeen:            c.last,
		}
		for command := range c.outer {
			row.OuterCommands = append(row.OuterCommands, command)
		}
		sort.Strings(row.OuterCommands)
		if row.FirstSeen.IsZero() {
			row.FirstSeen = now
		}
		if row.LastSeen.IsZero() {
			row.LastSeen = now
		}
		if summary := envSummary(c.failEnvs); summary != nil {
			row.EnvSummaryJSON = string(domain.MustCanonicalJSON(summary))
		}
		row.HypothesesJSON = string(domain.MustCanonicalJSON(hyps))
		row.EvidenceBreakdownJSON = string(domain.MustCanonicalJSON(c.breakdown))
		variants := make([]domain.FailureEnvironmentVariant, 0, len(c.variants))
		for _, v := range c.variants {
			item := domain.FailureEnvironmentVariant{Environment: v.env, Summary: envSummary([]domain.EnvironmentFingerprint{v.env}), Count: v.count}
			if !v.first.IsZero() {
				item.FirstSeen = v.first.UTC().Format(time.RFC3339)
			}
			if !v.last.IsZero() {
				item.LastSeen = v.last.UTC().Format(time.RFC3339)
			}
			variants = append(variants, item)
		}
		sort.Slice(variants, func(i, j int) bool {
			if variants[i].Count != variants[j].Count {
				return variants[i].Count > variants[j].Count
			}
			return variants[i].Environment.Hash() < variants[j].Environment.Hash()
		})
		row.EnvVariantsJSON = string(domain.MustCanonicalJSON(variants))
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

// isESMCode reports whether an error code NAMES the module system.
//
// It used to match ERR_MODULE_NOT_FOUND and anything containing "ESM", and
// that is the whole reason to be careful here: production carries 303
// clusters with a named cause and 2,246 with UNKNOWN, and every one of the
// 303 is ERR_MODULE_NOT_FOUND. There has never been an ERR_REQUIRE_ESM
// cluster. The entire published output of this classifier came from a code
// the function was not written for.
//
// The distribution below was reasoned about for ERR_REQUIRE_ESM, which names
// the module system: the code already asserts CONFIGURATION and the numbers
// only shade it. ERR_MODULE_NOT_FOUND says a specifier did not resolve, and
// its usual causes are a package moving or dropping a subpath in its exports
// map, or an install that did not finish. Putting .72 on CONFIGURATION and
// .07 on LIBRARY_REGRESSION is backwards for it, so the page printed "your
// module configuration is wrong" over a library that had quietly moved an
// export.
//
// The substring branch was worse than the misreading: strings.Contains(u,
// "ESM") claimed ESMTP_TIMEOUT -- an SMTP timeout -- as a module-system
// configuration fault.
//
// Nothing replaces those 303. A flat spread over CONFIGURATION, RESOURCE and
// LIBRARY_REGRESSION would be a way of writing UNKNOWN that renders as three
// confident chips, because the page drops the UNKNOWN residual and the reader
// never sees the mass that was withheld.
func isESMCode(code string) bool {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "ERR_REQUIRE_ESM", "ERR_UNSUPPORTED_DIR_IMPORT":
		return true
	}
	return false
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
