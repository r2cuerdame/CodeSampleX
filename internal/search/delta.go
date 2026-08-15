package search

import (
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// buildDelta renders the §11.5 Exact / Different lists. The LLM reasons
// over these deltas instead of re-deriving the whole comparison.
func buildDelta(rel pkgRel, reqP, samP domain.PURL, dims []dimComparison, cd contextDelta) (exact, different []string) {
	exact, different = []string{}, []string{}

	switch rel {
	case relExactVersion, relMajorMinor:
		exact = append(exact, samP.Name+" "+samP.MajorMinor())
	case relPackageOnly:
		// The package matched by name and nothing established its version, so
		// neither an exact claim nor a difference would be true.
		different = append(different,
			"Sample uses "+samP.Name+" (version not established by this shard)")
	case relMajor, relMajorDiff:
		different = append(different,
			"Sample uses "+samP.Name+" "+samP.MajorMinor(),
			"Current project uses "+reqP.Name+" "+reqP.MajorMinor())
	case relNone:
		// The caller named packages and the sample shares none of them. That
		// demotes the result to REFERENCE_ONLY, and the demotion was the only
		// sign of it: the delta came back empty, so the reader saw a result
		// marked "reference only" with nothing said about why. Asked about
		// react against a react-dom sample, the honest line is that they are
		// different packages, and it is the whole reason the grade dropped.
		if samP.Name != "" && reqP.Name != "" {
			different = append(different,
				"Sample uses "+samP.Name+" "+samP.MajorMinor(),
				"Current project uses "+reqP.Name+" "+reqP.MajorMinor()+" — a different package")
		}
	}

	for _, d := range dims {
		if d.equal {
			if d.exactEntry != "" {
				exact = append(exact, d.exactEntry)
			}
			continue
		}
		if d.samShow != "" || d.reqShow != "" {
			different = append(different,
				"Sample uses "+d.samShow,
				"Current project uses "+d.reqShow)
		}
	}

	// browserAdapt as well as mismatch. Only a mismatch was rendered, so a
	// browser-major difference — which CAPS the grade at
	// ADAPTATION_REQUIRED and puts "verify in safari 19" in the adaptation
	// list — was left out of the delta entirely: safari 19 caller, safari
	// 15 sample, and the answer came back ADAPTATION_REQUIRED with
	// Different empty and the sample's own browser never shown anywhere.
	// The reader was told to adapt without being told to what.
	if (cd.mismatch || cd.browserAdapt) && cd.samShow != "" && cd.reqShow != "" {
		different = append(different,
			"Sample uses "+cd.samShow,
			"Current project uses "+cd.reqShow)
	}

	return dedupe(exact), dedupe(different)
}

// dedupe removes repeated entries while preserving order (a runtime-name
// difference and a context mismatch can render the same pair).
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// runtimeShow renders "node 22" — major bucket, §11.5 style.
func runtimeShow(name, version string) string {
	if name == "" {
		return ""
	}
	// The release LINE, not the major. The comparison was fixed to treat
	// go1.9 and go1.26 as different lines, and then printed both of them as
	// "go 1" -- so the delta listed a difference whose two sides read
	// identically, which is worse than saying nothing.
	if line := releaseLineOf(name, version); line != "" {
		return name + " " + line
	}
	return name
}

// langShow renders "typescript 5.9" — major.minor bucket.
func langShow(name, version string) string {
	if mm := majorMinorOf(version); mm != "" {
		return name + " " + mm
	}
	return name
}

// osShow renders "windows 11".
func osShow(e domain.EnvironmentFingerprint) string {
	if e.OSVersionBucket != "" {
		return e.OS + " " + e.OSVersionBucket
	}
	return e.OS
}

// majorOf trims "22.18.1" → "22", dropping pre-release/build suffixes.
func majorOf(v string) string {
	if v == "" {
		return ""
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	return strings.SplitN(v, ".", 2)[0]
}

// releaseLineOf is the version segment that actually identifies a release
// line for a given toolchain.
//
// For most runtimes the major segment is the line: node 22, python is not
// one of them. Go, Python, Elixir and Dart put their entire release history
// in the SECOND segment -- go1.9 and go1.26 are seven years apart and both
// "1"; python 3.6 and 3.12 both "3" -- so comparing majors alone called
// them equal and printed "go 1" in the list of things that MATCH.
//
// The server-side grader has used this rule since it was fixed there. The
// client did not, so the same request graded EXACT here and
// ADAPTATION_REQUIRED there: two answers to one question, and the wrong one
// went to whoever was running the MCP.
func releaseLineOf(toolchain, version string) string {
	if version == "" {
		return ""
	}
	if i := strings.IndexAny(version, "-+"); i >= 0 {
		version = version[:i]
	}
	switch strings.ToLower(toolchain) {
	case "go", "golang", "python", "elixir", "dart":
		segs := strings.SplitN(version, ".", 3)
		if len(segs) >= 2 {
			return segs[0] + "." + segs[1]
		}
		return segs[0]
	}
	return majorOf(version)
}

// majorMinorOf trims "5.9.2" → "5.9"; single segments stay as-is.
func majorMinorOf(v string) string {
	if v == "" {
		return ""
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	segs := strings.SplitN(v, ".", 3)
	if len(segs) >= 2 {
		return segs[0] + "." + segs[1]
	}
	return segs[0]
}

// humanEnvSummary renders a failure cluster's env summary in §11.5 style:
// {"moduleSystem":"esm","runtime":"node@18"} → "node 18 + esm".
// Runtime leads; remaining keys follow in sorted-key order.
func humanEnvSummary(summary map[string]string) string {
	if len(summary) == 0 {
		return "unknown environment"
	}
	var parts []string
	if rt, ok := summary["runtime"]; ok {
		parts = append(parts, strings.ReplaceAll(rt, "@", " "))
	}
	keys := make([]string, 0, len(summary))
	for k := range summary {
		if k == "runtime" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, summary[k])
	}
	return strings.Join(parts, " + ")
}
