package fixclaims

import (
	"regexp"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Probe is one execution the planner asks Farm for: this release, in this
// environment, with the candidate's reproducer.
type Probe struct {
	Version     string      `json:"version"`
	Environment Environment `json:"environment"`
	// Reason says why the planner wants it, so a worker log and an operator
	// can tell a first pair from a boundary walk.
	Reason string `json:"reason"`
}

// Probe reasons.
const (
	ProbeBadVersion      = "bad-version"
	ProbeFixedVersion    = "fixed-version"
	ProbeFindFirstBad    = "find-first-bad"
	ProbeRegressionWatch = "regression-watch"
	ProbeLaterFix        = "later-fix"
	ProbeEnvironmentHint = "environment-hint"
)

// Policy bounds the expansion. The defaults are the Phase 0 numbers: a pair
// is two runs, and a candidate that has shown a signal may spend at most
// ten more before the record is closed as it stands.
type Policy struct {
	// MaxRuns is the hard cap on runs per candidate across every
	// environment and version. Expansion stops at it however much signal
	// remains; the record keeps the boundary it found.
	MaxRuns int
	// MaxProbesPerTurn bounds one handout, so a worker never receives a
	// whole matrix.
	MaxProbesPerTurn int
	// OS lists the operating systems the worker asking can run. Empty means
	// any. A probe for an OS the worker cannot run is not handed to it --
	// an UNRUNNABLE verdict from a lane that never existed would be
	// recorded as a fact about the package.
	OS []string
}

// DefaultPolicy is the shipped expansion policy.
func DefaultPolicy() Policy { return Policy{MaxRuns: 12, MaxProbesPerTurn: 4} }

func (p Policy) runs(env Environment) bool {
	if len(p.OS) == 0 {
		return true
	}
	for _, os := range p.OS {
		if strings.EqualFold(os, env.OS) {
			return true
		}
	}
	return false
}

// DefaultEnvironment is the environment the first pair runs in when the
// claim carries no hint: the Farm's linux container lane.
var DefaultEnvironment = Environment{OS: "linux"}

// Plan decides which runs Farm should perform next for a candidate, given
// what has already run and which releases of the package this network
// knows about. It is adaptive rather than Cartesian:
//
//  1. Until the pair (claimed-bad, claimed-fixed) has run in the first
//     environment, the pair is all that is asked for. An OS named in the
//     claim's environment hints adds the pair in that OS as well.
//  2. Only once some environment shows REPRODUCED_AND_FIXED or
//     REPRODUCED_NOT_FIXED does the planner walk the boundary: one known
//     release below the lowest FAIL (find-first-bad), one known release
//     above the highest PASS (regression-watch), and for a claim not fixed
//     where it said, one release above the claimed fix (later-fix).
//  3. NOT_REPRODUCED, UNRUNNABLE and AMBIGUOUS ask for nothing more: there
//     is no signal to expand from, and the hard attempt cap is what
//     retires the candidate.
//
// known is every release of the package the caller can name, in any
// order; the claimed versions are added to it. Nothing here consults a
// registry.
func Plan(c Candidate, runs []Run, known []string, pol Policy) []Probe {
	c = c.Normalized()
	if pol.MaxRuns <= 0 {
		def := DefaultPolicy()
		pol.MaxRuns, pol.MaxProbesPerTurn = def.MaxRuns, def.MaxProbesPerTurn
	}
	if len(runs) >= pol.MaxRuns {
		return nil
	}
	budget := pol.MaxRuns - len(runs)
	if pol.MaxProbesPerTurn > 0 && budget > pol.MaxProbesPerTurn {
		budget = pol.MaxProbesPerTurn
	}
	ev := Evaluate(c, runs)
	badVersion := c.ClaimedBadVersion
	if badVersion == "" {
		badVersion = previousKnown(known, c.ClaimedFixedVersion)
	}
	ran := map[string]bool{}
	for _, r := range runs {
		ran[r.Environment.Key()+"@"+r.Version] = true
	}
	var probes []Probe
	want := func(v string, env Environment, reason string) {
		if v == "" || len(probes) >= budget || !pol.runs(env) {
			return
		}
		key := env.Key() + "@" + v
		if ran[key] {
			return
		}
		ran[key] = true
		probes = append(probes, Probe{Version: v, Environment: env, Reason: reason})
	}

	envs := []Environment{DefaultEnvironment}
	for _, os := range hintedOS(c.EnvironmentHints) {
		if os != DefaultEnvironment.OS {
			envs = append(envs, Environment{OS: os})
		}
	}
	// A claim tied to a runtime version ("python-3.12") gets the pair at
	// that version too, in the default OS. The unversioned pair still runs
	// first: it is the lane's default image, and the versioned one is only
	// meaningful beside it.
	envs = append(envs, hintedRuntimes(c.EnvironmentHints)...)
	// 1. The pair, in every environment the claim asks for.
	for i, env := range envs {
		reasonBad, reasonFixed := ProbeBadVersion, ProbeFixedVersion
		if i > 0 {
			reasonBad, reasonFixed = ProbeEnvironmentHint, ProbeEnvironmentHint
		}
		want(badVersion, env, reasonBad)
		want(c.ClaimedFixedVersion, env, reasonFixed)
	}
	if len(probes) > 0 {
		return probes
	}
	// 2. Walk the boundary only where the pair produced a signal.
	for _, res := range ev.Environments {
		switch res.Outcome {
		case PairReproducedAndFixed:
			want(previousKnown(known, res.FirstObservedBad), res.Environment, ProbeFindFirstBad)
			highestPass := res.FirstObservedGood
			for _, r := range runs {
				if r.Environment.Key() == res.Environment.Key() && r.Verdict == VerdictPass && domain.CompareVersions(r.Version, highestPass) > 0 {
					highestPass = r.Version
				}
			}
			want(nextKnown(known, highestPass), res.Environment, ProbeRegressionWatch)
		case PairReproducedNotFixed:
			highestFail := res.LastObservedBad
			want(nextKnown(known, highestFail), res.Environment, ProbeLaterFix)
		}
	}
	return probes
}

// hintedOS returns the operating systems named in the environment hints,
// in a fixed order, so a claim tied to Windows gets a Windows pair without
// the whole matrix coming with it.
func hintedOS(hints []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range hints {
		os := ""
		switch {
		case strings.HasPrefix(h, "win"):
			os = "windows"
		case strings.HasPrefix(h, "mac"), strings.HasPrefix(h, "darwin"), strings.HasPrefix(h, "osx"):
			os = "darwin"
		case strings.HasPrefix(h, "linux"), strings.HasPrefix(h, "ubuntu"), strings.HasPrefix(h, "debian"), strings.HasPrefix(h, "alpine"):
			os = "linux"
		}
		if os != "" && !seen[os] {
			seen[os] = true
			out = append(out, os)
		}
	}
	sort.Strings(out)
	return out
}

// runtimeHint is the shape the collector writes for a versioned runtime
// hint: "python-3.12", "node-22", "java-21". A bare runtime ("node") names
// no version and adds no environment.
var runtimeHint = regexp.MustCompile(`^(node|python|java|go|rust|ruby|php|dart|elixir)-v?(\d+(?:\.\d+)?)`)

// hintedRuntimes returns one environment per versioned runtime hint, in
// the default OS, in a fixed order. Two hints for the same runtime and
// version are one environment.
func hintedRuntimes(hints []string) []Environment {
	var out []Environment
	seen := map[string]bool{}
	for _, h := range hints {
		m := runtimeHint.FindStringSubmatch(strings.ToLower(strings.TrimSpace(h)))
		if m == nil {
			continue
		}
		env := Environment{OS: DefaultEnvironment.OS, Runtime: m[1], RuntimeVersion: m[2]}
		if seen[env.Key()] {
			continue
		}
		seen[env.Key()] = true
		out = append(out, env)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func sortedKnown(known []string, extra ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string(nil), known...), extra...) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] || !domain.ConcreteResolvedVersion(v) {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return domain.CompareVersions(out[i], out[j]) < 0 })
	return out
}

// previousKnown is the highest known release strictly below v, or "".
func previousKnown(known []string, v string) string {
	if v == "" {
		return ""
	}
	prev := ""
	for _, k := range sortedKnown(known) {
		if domain.CompareVersions(k, v) < 0 {
			prev = k
		}
	}
	return prev
}

// nextKnown is the lowest known release strictly above v, or "".
func nextKnown(known []string, v string) string {
	if v == "" {
		return ""
	}
	for _, k := range sortedKnown(known) {
		if domain.CompareVersions(k, v) > 0 {
			return k
		}
	}
	return ""
}
