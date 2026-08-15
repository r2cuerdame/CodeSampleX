package search

import (
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
func compareEnv(req, sam domain.EnvironmentFingerprint, ecosystem string) []dimComparison {
	out := []dimComparison{}

	if req.Runtime != "" && sam.Runtime != "" {
		reqShow := runtimeShow(req.Runtime, req.RuntimeVersion)
		samShow := runtimeShow(sam.Runtime, sam.RuntimeVersion)
		switch {
		case strings.EqualFold(req.Runtime, sam.Runtime) &&
			majorOf(req.RuntimeVersion) == majorOf(sam.RuntimeVersion):
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
			majorOf(req.CompilerVersion) == majorOf(sam.CompilerVersion)) {
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

	if req.OS != "" && sam.OS != "" && !strings.EqualFold(req.OS, sam.OS) {
		// OS is informational unless a failure-cluster boundary says
		// otherwise (§11.4) — the cluster path handles that demotion.
		out = append(out, dimComparison{samShow: osShow(sam), reqShow: osShow(req)})
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
	if req.Arch != "" && sam.Arch != "" && !strings.EqualFold(req.Arch, sam.Arch) {
		out = append(out, dimComparison{samShow: sam.Arch, reqShow: req.Arch})
	}
	if req.Libc != "" && sam.Libc != "" && !strings.EqualFold(req.Libc, sam.Libc) {
		out = append(out, dimComparison{samShow: sam.Libc, reqShow: req.Libc})
	}

	return out
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
