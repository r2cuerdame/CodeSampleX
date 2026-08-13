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
		case strings.EqualFold(req.Runtime, sam.Runtime):
			// Same runtime, different major: not an enumerable adaptation.
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
		if strings.EqualFold(req.Language, sam.Language) &&
			majorMinorOf(req.LanguageVersion) == majorMinorOf(sam.LanguageVersion) {
			out = append(out, dimComparison{equal: true, exactEntry: samShow})
		} else {
			out = append(out, dimComparison{samShow: samShow, reqShow: reqShow})
		}
	}

	if req.Compiler != "" && sam.Compiler != "" {
		reqShow := runtimeShow(req.Compiler, req.CompilerVersion)
		samShow := runtimeShow(sam.Compiler, sam.CompilerVersion)
		if strings.EqualFold(req.Compiler, sam.Compiler) &&
			majorOf(req.CompilerVersion) == majorOf(sam.CompilerVersion) {
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
	for _, d := range dims {
		if d.equal {
			continue
		}
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
