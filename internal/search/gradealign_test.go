package search

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func gradeOf(t *testing.T, req, sam domain.EnvironmentFingerprint, ecosystem string, rel pkgRel, inferred bool) (domain.MatchGrade, []dimComparison) {
	t.Helper()
	dims := compareEnv(req, sam, ecosystem, inferred)
	g, _ := buildGrade(rel, dims, compareContext(req, sam), false)
	return g, dims
}

func dimsMention(dims []dimComparison, needle string) bool {
	for _, d := range dims {
		if strings.Contains(strings.ToLower(d.samShow+" "+d.reqShow+" "+d.exactEntry), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// The server and the client answered the same question one rung apart.
//
// C7 puts a MINOR version difference on the ADAPTATION_REQUIRED rung by
// name, and the server grader implements exactly that — a different release
// line becomes "verify on <runtime>", capped at ADAPTATION_REQUIRED. The
// client forced REFERENCE_ONLY for the same input.
//
// A previous commit set out to close this divergence and got half of it: the
// equality test above was moved from majorOf to releaseLineOf, and the branch
// below it was left forcing REFERENCE_ONLY. For node the two agree, so
// nothing moved; for python and go they do not, and the correct sample for a
// jinja2 error came back REFERENCE_ONLY because the caller ran 3.10 and the
// sample ran 3.12.
func TestAMinorRuntimeDifferenceIsAnAdaptationNotAReference(t *testing.T) {
	req := domain.EnvironmentFingerprint{Ecosystem: "pypi", Runtime: "python", RuntimeVersion: "3.10.11"}
	sam := domain.EnvironmentFingerprint{Ecosystem: "pypi", Runtime: "python", RuntimeVersion: "3.12.4"}

	g, dims := gradeOf(t, req, sam, "pypi", relExactVersion, false)
	if g == domain.GradeReferenceOnly {
		t.Errorf("grade = %s, want the client to agree with the server (ADAPTATION_REQUIRED)", g)
	}
	if g != domain.GradeAdaptationRequired {
		t.Errorf("grade = %s, want ADAPTATION_REQUIRED", g)
	}
	if !dimsMention(dims, "python") {
		t.Error("the runtime difference is not shown to the reader at all")
	}
}

// Same runtime and the same release line is still an exact dimension.
func TestSameReleaseLineStaysExact(t *testing.T) {
	req := domain.EnvironmentFingerprint{Ecosystem: "pypi", Runtime: "python", RuntimeVersion: "3.10.11"}
	sam := domain.EnvironmentFingerprint{Ecosystem: "pypi", Runtime: "python", RuntimeVersion: "3.10.2"}
	if g, _ := gradeOf(t, req, sam, "pypi", relExactVersion, false); g != domain.GradeExact {
		t.Errorf("grade = %s, want EXACT", g)
	}
}

// A sample from another ecosystem came back COMPATIBLE, and its delta did not
// even mention the ecosystem.
//
// From a pypi project holding only jinja2, "ModuleNotFoundError: No module
// named 'requests'" returned an npm/jest sample as COMPATIBLE, and
// "TypeError ... in pandas" returned npm/luxon the same way. envAskedAbout
// blanks the request's ecosystem-scoped dimensions when the candidate belongs
// to another ecosystem — which is right for runtime and language, and wrong
// for the ecosystem itself. Blanking the one dimension that says WHICH
// ecosystem removed the only line that made the answer obviously about
// something else.
//
// Keeping it is not a return to the old behaviour: an ecosystem the caller
// never stated, only inferred from the directory they happen to be sitting
// in, caps the grade at ADAPTATION_REQUIRED instead of forcing
// REFERENCE_ONLY. Using an npm sample to fix a Python import IS an
// adaptation, and it is not a claim that the sample fits.
func TestACrossEcosystemAnswerSaysSoAndDoesNotClaimToFit(t *testing.T) {
	req := domain.EnvironmentFingerprint{Ecosystem: "pypi", OS: "windows", Arch: "x64"}
	sam := domain.EnvironmentFingerprint{Ecosystem: "npm", OS: "linux", Arch: "x64"}

	asked := envAskedAbout(req, "npm", true)
	if asked.Ecosystem == "" {
		t.Fatal("the ecosystem was blanked, so nothing can report the mismatch")
	}
	// The dimensions that describe how the CALLER's own ecosystem runs are
	// still dropped — that is the fix this one must not undo.
	if asked.Runtime != "" || asked.Language != "" || asked.PackageManager != "" {
		t.Errorf("caller's ecosystem-scoped dimensions survived: %+v", asked)
	}

	g, dims := gradeOf(t, asked, sam, "npm", relUnspecified, true)
	if g == domain.GradeCompatible || g == domain.GradeExact {
		t.Errorf("grade = %s, want a cross-ecosystem answer not to claim it fits", g)
	}
	if !dimsMention(dims, "ecosystem") {
		t.Error("the delta never names the ecosystem difference")
	}
}

// When the caller STATED their ecosystem, a mismatch is still REFERENCE_ONLY:
// they asked about that ecosystem and this is not it.
func TestAStatedEcosystemMismatchIsStillReferenceOnly(t *testing.T) {
	req := domain.EnvironmentFingerprint{Ecosystem: "pypi", OS: "linux"}
	sam := domain.EnvironmentFingerprint{Ecosystem: "npm", OS: "linux"}

	asked := envAskedAbout(req, "npm", false) // not inferred: the caller said so
	if g, _ := gradeOf(t, asked, sam, "npm", relUnspecified, false); g != domain.GradeReferenceOnly {
		t.Errorf("grade = %s, want REFERENCE_ONLY when the caller stated the ecosystem", g)
	}
}
