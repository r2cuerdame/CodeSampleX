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

// A package the caller NAMED is a package they asked about, whatever
// directory they are standing in.
//
// envAskedAbout softens the caller's ecosystem when the environment was only
// inferred from the working directory — and internal/cli/search.go marks
// every search that way, including one carrying --package. So
// `csx search "..." --package pkg:pypi/polars@1.9.0` run from an npm checkout
// had the polars sample demoted and, worse, handed the agent an adaptation
// reading "translate from pypi to npm": an instruction to port a Python
// sample to JavaScript, for a package the caller had just named.
//
// The mismatch cap is for a candidate the caller never mentioned. Naming one
// answers the question the inference was guessing at.
func TestAnExplicitlyNamedPackageIsNotTreatedAsTheWrongEcosystem(t *testing.T) {
	req := domain.EnvironmentFingerprint{Ecosystem: "npm", OS: "linux", Arch: "x64"}

	// Inferred npm environment, but the caller named a pypi package and this
	// candidate is that ecosystem.
	asked := envAskedAboutNamed(req, "pypi", true, true)
	if asked.Ecosystem != "" {
		t.Errorf("kept the caller's inferred ecosystem %q against a package they named", asked.Ecosystem)
	}

	sam := domain.EnvironmentFingerprint{Ecosystem: "pypi", OS: "linux", Arch: "x64"}
	g, dims := gradeOf(t, asked, sam, "pypi", relExactVersion, true)
	if g == domain.GradeReferenceOnly || g == domain.GradeAdaptationRequired {
		t.Errorf("grade = %s, want a named package not to be demoted for the caller's directory", g)
	}
	for _, d := range dims {
		if strings.Contains(d.adaptation, "translate from") {
			t.Errorf("told the agent to port the sample to another ecosystem: %q", d.adaptation)
		}
	}
}

// A candidate the caller did NOT name still gets the cap — that is the fix
// this must not undo.
func TestAnUnnamedForeignCandidateStillCaps(t *testing.T) {
	req := domain.EnvironmentFingerprint{Ecosystem: "pypi", OS: "windows", Arch: "x64"}
	asked := envAskedAboutNamed(req, "npm", true, false)
	if asked.Ecosystem == "" {
		t.Fatal("blanked the ecosystem for a candidate the caller never named")
	}
	sam := domain.EnvironmentFingerprint{Ecosystem: "npm", OS: "linux", Arch: "x64"}
	if g, _ := gradeOf(t, asked, sam, "npm", relUnspecified, true); g == domain.GradeCompatible || g == domain.GradeExact {
		t.Errorf("grade = %s, want a cross-ecosystem answer not to claim it fits", g)
	}
}
