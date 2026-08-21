package search

import (
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// dimComparison is the verdict for one environment dimension:
//   - equal + exactEntry     → Exact list
//   - differ, samShow/reqShow → Different list
//   - differ + adaptation     → caps the grade at ADAPTATION_REQUIRED
//   - differ + refOnly        → forces REFERENCE_ONLY
//
// A dimension is only compared when both sides declare it — sparse
// fingerprints omit meaningless dimensions, and absence is unknown, not a
// difference.
type dimComparison struct {
	equal      bool
	exactEntry string
	samShow    string
	reqShow    string
	adaptation string
	refOnly    bool
}

// compareEnv evaluates the non-context dimensions. Sensitivity per C7:
// moduleSystem+runtime for npm, runtime for pypi, toolchain (compiler) for
// cargo/golang; moduleSystem and packageManager differences are enumerable
// adaptations; a runtime or toolchain major difference is not.
func compareEnv(req, sam domain.EnvironmentFingerprint, ecosystem string, ecosystemInferred bool) []dimComparison {
	out := []dimComparison{}

	if req.Ecosystem != "" && sam.Ecosystem != "" {
		switch {
		case strings.EqualFold(req.Ecosystem, sam.Ecosystem):
			out = append(out, dimComparison{equal: true, exactEntry: "ecosystem " + strings.ToLower(sam.Ecosystem)})
		case ecosystemInferred:
			// The caller never said which ecosystem they were asking about;
			// we read it off the directory they happened to be sitting in.
			// That is worth saying and is not worth refusing over: somebody
			// in a Go checkout asking how to freeze the clock in a Python
			// test is asking a Python question, and forcing REFERENCE_ONLY
			// on the whole answer is what made three of four such queries
			// return NO_SAFE_MATCH from inside a project.
			//
			// It is still not a fit. Using an npm sample to fix a Python
			// import is an adaptation, and saying so caps the grade instead
			// of endorsing it.
			out = append(out, dimComparison{
				samShow:    "ecosystem " + strings.ToLower(sam.Ecosystem),
				reqShow:    "ecosystem " + strings.ToLower(req.Ecosystem),
				adaptation: "translate from " + strings.ToLower(sam.Ecosystem) + " to " + strings.ToLower(req.Ecosystem),
			})
		default:
			// The caller stated the ecosystem. This is not it.
			out = append(out, dimComparison{
				samShow: "ecosystem " + strings.ToLower(sam.Ecosystem),
				reqShow: "ecosystem " + strings.ToLower(req.Ecosystem), refOnly: true,
			})
		}
	}

	if req.Runtime != "" && sam.Runtime != "" {
		reqShow := runtimeShow(req.Runtime, req.RuntimeVersion)
		samShow := runtimeShow(sam.Runtime, sam.RuntimeVersion)
		switch {
		case strings.EqualFold(req.Runtime, sam.Runtime) &&
			releaseLineOf(req.Runtime, req.RuntimeVersion) ==
				releaseLineOf(sam.Runtime, sam.RuntimeVersion):
			out = append(out, dimComparison{equal: true, exactEntry: samShow})
		// A version nobody stated is not a version that differs. The guard
		// above only checks that both sides name a RUNTIME, then compares
		// their VERSIONS — so a sample declaring runtime "node" with no
		// version was read as a different major from any versioned request
		// and forced to REFERENCE_ONLY, with the delta printing "Sample
		// uses node, current project uses node 22" as though that were a
		// difference.
		//
		// Found by asking the live network a real question: the sample that
		// answered it exactly came back REFERENCE_ONLY at 0.35 while an
		// unrelated sample for the same package took the top slot at 0.96,
		// purely because the unrelated one happened to state a version.
		case strings.EqualFold(req.Runtime, sam.Runtime) &&
			(req.RuntimeVersion == "" || sam.RuntimeVersion == ""):
			out = append(out, dimComparison{equal: true, exactEntry: samShow})
		case strings.EqualFold(req.Runtime, sam.Runtime) &&
			majorOf(req.RuntimeVersion) == majorOf(sam.RuntimeVersion):
			// Same runtime, same major, different release line. C7 puts a
			// MINOR version difference on the ADAPTATION_REQUIRED rung by
			// name; this branch used to force REFERENCE_ONLY for it.
			//
			// The distinction matters because releaseLineOf buckets by major
			// for node and by major.minor for python and go. Reading "a
			// different release line" as "a different major" is true for
			// node and false for the others, so the sample that answered a
			// jinja2 error exactly came back REFERENCE_ONLY purely because
			// the caller ran python 3.10 and it ran 3.12.
			out = append(out, dimComparison{
				samShow: samShow, reqShow: reqShow,
				adaptation: "verify on " + strings.ToLower(reqShow),
			})
		case strings.EqualFold(req.Runtime, sam.Runtime):
			// Same runtime, KNOWN different major: not an enumerable adaptation.
			out = append(out, dimComparison{samShow: samShow, reqShow: reqShow, refOnly: true})
		default:
			// Different runtime NAME is an execution-context divergence;
			// compareContext judges it. Record display only.
			out = append(out, dimComparison{samShow: samShow, reqShow: reqShow})
		}
	}

	if req.Language != "" && sam.Language != "" {
		reqShow := langShow(req.Language, req.LanguageVersion)
		samShow := langShow(sam.Language, sam.LanguageVersion)
		sameLang := strings.EqualFold(req.Language, sam.Language)
		// Same reasoning as the runtime above: an unstated version is not a
		// version that differs.
		unknownVersion := req.LanguageVersion == "" || sam.LanguageVersion == ""
		if sameLang && (unknownVersion ||
			majorMinorOf(req.LanguageVersion) == majorMinorOf(sam.LanguageVersion)) {
			out = append(out, dimComparison{equal: true, exactEntry: samShow})
		} else {
			out = append(out, dimComparison{samShow: samShow, reqShow: reqShow})
		}
	}

	if req.Compiler != "" && sam.Compiler != "" {
		reqShow := runtimeShow(req.Compiler, req.CompilerVersion)
		samShow := runtimeShow(sam.Compiler, sam.CompilerVersion)
		sameCompiler := strings.EqualFold(req.Compiler, sam.Compiler)
		unknownCompilerVersion := req.CompilerVersion == "" || sam.CompilerVersion == ""
		if sameCompiler && (unknownCompilerVersion ||
			releaseLineOf(req.Compiler, req.CompilerVersion) ==
				releaseLineOf(sam.Compiler, sam.CompilerVersion)) {
			out = append(out, dimComparison{equal: true, exactEntry: samShow})
		} else {
			d := dimComparison{samShow: samShow, reqShow: reqShow}
			if ecosystem == "cargo" || ecosystem == "golang" {
				d.refOnly = true // toolchain is the sensitive dim here
			}
			out = append(out, d)
		}
	}

	if req.ModuleSystem != "" && sam.ModuleSystem != "" {
		if strings.EqualFold(req.ModuleSystem, sam.ModuleSystem) {
			out = append(out, dimComparison{equal: true, exactEntry: strings.ToUpper(sam.ModuleSystem)})
		} else {
			out = append(out, dimComparison{
				samShow:    strings.ToUpper(sam.ModuleSystem),
				reqShow:    strings.ToUpper(req.ModuleSystem),
				adaptation: "Import syntax only",
			})
		}
	}

	if req.PackageManager != "" && sam.PackageManager != "" {
		if strings.EqualFold(req.PackageManager, sam.PackageManager) {
			out = append(out, dimComparison{equal: true, exactEntry: strings.ToLower(sam.PackageManager)})
		} else {
			out = append(out, dimComparison{
				samShow:    strings.ToLower(sam.PackageManager),
				reqShow:    strings.ToLower(req.PackageManager),
				adaptation: "Package manager commands (" + strings.ToLower(sam.PackageManager) + " → " + strings.ToLower(req.PackageManager) + ")",
			})
		}
	}

	// Agreement is recorded, not only difference.
	//
	// These three were appended ONLY when they disagreed, so a caller whose
	// OS, architecture and libc all matched the sample saw none of them in
	// the Exact list — the answer said "undici 8.10, node" and stayed
	// silent about the machine, which is the part the reader came for. And
	// a request that knows ONLY os/arch/libc produced no comparable
	// dimension at all, so a perfect match was capped at COMPATIBLE for
	// want of anything to compare.
	if req.OS != "" && sam.OS != "" {
		if strings.EqualFold(req.OS, sam.OS) {
			out = append(out, dimComparison{equal: true, exactEntry: osShow(sam)})
		} else {
			// OS is informational unless a failure-cluster boundary says
			// otherwise (§11.4) — the cluster path handles that demotion.
			out = append(out, dimComparison{samShow: osShow(sam), reqShow: osShow(req)})
		}
	}

	// Architecture and libc were not compared at all, so a caller on
	// glibc/x64 was told a sample verified on musl/arm64 was an EXACT match
	// with an empty difference list. These are the two dimensions that most
	// often decide whether a package with a native module loads at all —
	// the whole reason the fingerprint carries them — and the grade was
	// silent about both. The server-side search compares them; this one
	// never did, so the same input got two different answers depending on
	// which path the caller happened to take.
	//
	// They are recorded as differences rather than forced to
	// REFERENCE_ONLY: a pure-source sample really does carry across, and
	// the honest statement is "this ran somewhere else, here is where",
	// which costs the EXACT claim and keeps the answer.
	if req.Arch != "" && sam.Arch != "" {
		if strings.EqualFold(req.Arch, sam.Arch) {
			out = append(out, dimComparison{equal: true, exactEntry: sam.Arch})
		} else {
			out = append(out, dimComparison{samShow: sam.Arch, reqShow: req.Arch})
		}
	}
	if req.Libc != "" && sam.Libc != "" {
		switch {
		case !strings.EqualFold(req.Libc, sam.Libc):
			out = append(out, dimComparison{samShow: sam.Libc, reqShow: req.Libc})
		// Same family, and glibc is only compatible in ONE direction: a
		// newer glibc runs a binary built against an older one, never the
		// reverse. A sample verified on 2.39 hands a caller on 2.28 a
		// "GLIBC_2.34 not found" at load time, and it was reported as an
		// exact libc match because both said "glibc".
		//
		// This is the second most common reason a native module refuses to
		// load on Linux, after musl itself, and prebuilt wheels are named
		// for it: manylinux2014 IS glibc 2.17.
		case olderLibc(req.LibcVersion, sam.LibcVersion):
			out = append(out, dimComparison{
				samShow: libcShow(sam), reqShow: libcShow(req),
			})
		default:
			out = append(out, dimComparison{equal: true, exactEntry: libcShow(sam)})
		}
	}

	return out
}

// libcShow renders "glibc 2.35", or just the family when no version is
// known — never an invented one.
func libcShow(e domain.EnvironmentFingerprint) string {
	if e.LibcVersion == "" {
		return e.Libc
	}
	return e.Libc + " " + e.LibcVersion
}

// olderLibc reports whether the CALLER's glibc is older than the one the
// sample ran on, which is the direction that breaks. Unknown on either
// side is not a difference: an unstated version is not a version that
// differs.
func olderLibc(reqVer, samVer string) bool {
	if reqVer == "" || samVer == "" {
		return false
	}
	return compareLibcVersions(reqVer, samVer) < 0
}

// compareLibcVersions orders "2.28" before "2.35" numerically, so 2.9 does
// not sort above 2.35 the way a string comparison would.
func compareLibcVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// contextDelta is the execution-context axis verdict
// (docs/execution-context.md §5). The axis is ALWAYS sensitive.
type contextDelta struct {
	mismatch      bool // different context, or different engine — strong penalty
	browserAdapt  bool // same engine family/major delta — adaptable
	verifyIn      string
	majorDistance int // browserMajor distance ~ minor-version distance
	samShow       string
	reqShow       string
}

// contextOf resolves the open-vocabulary execution context of an env.
func contextOf(e domain.EnvironmentFingerprint) string {
	e = e.Normalize()
	if e.ExecutionContext != "" {
		return strings.ToLower(e.ExecutionContext)
	}
	if e.BrowserFamily != "" {
		return "browser"
	}
	if e.Runtime != "" {
		return strings.ToLower(e.Runtime)
	}
	return ""
}

// ctxDisplay renders the context for delta text: "safari 19", "node 22.18".
func ctxDisplay(e domain.EnvironmentFingerprint) string {
	if e.BrowserFamily != "" {
		if e.BrowserMajor != "" {
			return e.BrowserFamily + " " + e.BrowserMajor
		}
		return e.BrowserFamily
	}
	if l := e.ContextLabel(); l != "" {
		return l
	}
	return e.ExecutionContext
}

// compareContext judges the execution-context axis. Engine mismatch scores
// like a context mismatch; browserMajor distance like minor-version
// distance. When either side has no context information the axis cannot be
// judged and contributes nothing.
func compareContext(req, sam domain.EnvironmentFingerprint) contextDelta {
	var d contextDelta
	rc, sc := contextOf(req), contextOf(sam)
	if rc == "" || sc == "" {
		return d
	}
	d.reqShow, d.samShow = ctxDisplay(req), ctxDisplay(sam)
	if !strings.EqualFold(rc, sc) {
		d.mismatch = true
		d.verifyIn = d.reqShow
		return d
	}
	rf, sf := strings.ToLower(req.BrowserFamily), strings.ToLower(sam.BrowserFamily)
	if rf != "" && sf != "" {
		if rf != sf {
			re, se := req.Normalize().Engine, sam.Normalize().Engine
			if re != "" && se != "" && !strings.EqualFold(re, se) {
				d.mismatch = true
			} else {
				d.browserAdapt = true
			}
			d.verifyIn = d.reqShow
			return d
		}
		if req.BrowserMajor != "" && sam.BrowserMajor != "" && req.BrowserMajor != sam.BrowserMajor {
			d.browserAdapt = true
			d.verifyIn = d.reqShow
			d.majorDistance = intDistance(req.BrowserMajor, sam.BrowserMajor)
		}
	}
	return d
}

// buildGrade applies the C7 grade rules plus the execution-context caps:
//   - elevated failure in the requester env, package major difference, or a
//     non-adaptable sensitive difference → REFERENCE_ONLY
//   - any adaptable difference or context/engine mismatch → capped at
//     ADAPTATION_REQUIRED, with a "verify in <ctx>" entry on context deltas
//   - same major.minor and all sensitive dims equal → EXACT
//   - otherwise (same major / unversioned request) → COMPATIBLE
func buildGrade(rel pkgRel, dims []dimComparison, cd contextDelta, elevated bool) (domain.MatchGrade, []string) {
	adaptations := []string{}
	refOnly := elevated || rel == relMajorDiff || rel == relNone
	adapt := false
	// anyDifference is every non-equal dimension, including the ones carried
	// for display alone. EXACT means "nothing here differs from yours", and
	// only refOnly and adaptation differences were consulted — so a result
	// could be graded EXACT while its own Different list named the OS, the
	// architecture and the libc it had actually run on. The grade and the
	// delta were describing two different comparisons.
	anyDifference := false
	for _, d := range dims {
		if d.equal {
			continue
		}
		anyDifference = true
		if d.refOnly {
			refOnly = true
		}
		if d.adaptation != "" {
			adapt = true
			adaptations = append(adaptations, d.adaptation)
		}
	}
	if cd.mismatch || cd.browserAdapt {
		adapt = true
		if cd.verifyIn != "" {
			adaptations = append(adaptations, "verify in "+cd.verifyIn)
		}
	}
	switch {
	case refOnly:
		return domain.GradeReferenceOnly, adaptations
	case adapt:
		return domain.GradeAdaptationRequired, adaptations
	case rel == relExactVersion || rel == relMajorMinor:
		// EXACT is a claim about the ENVIRONMENT as much as the version:
		// nothing here differs from yours. A caller that supplied no
		// environment — every MCP client that omits the field — compared
		// nothing, and every dimension was skipped for want of a value on
		// one side. That produced an empty difference list, which read as
		// agreement, and the most confident grade the system has was
		// handed out on no evidence at all.
		//
		// Silence is not agreement. With nothing comparable the honest
		// ceiling is COMPATIBLE: the version is right, the machine is
		// unknown.
		if !anyComparable(dims) || anyDifference {
			return domain.GradeCompatible, adaptations
		}
		return domain.GradeExact, adaptations
	default:
		return domain.GradeCompatible, adaptations
	}
}

// equalFoldName compares package names case-insensitively (npm names are
// lowercase in practice; golang module paths are case-sensitive but the
// bang-encoded forms never reach purls here).
func equalFoldName(a, b string) bool {
	return strings.EqualFold(a, b)
}

// anyComparable reports whether any environment dimension actually had a
// value on both sides. compareEnv skips a dimension when either side is
// empty, so an all-empty request yields an empty comparison list that is
// indistinguishable from perfect agreement unless someone asks.
func anyComparable(dims []dimComparison) bool { return len(dims) > 0 }
