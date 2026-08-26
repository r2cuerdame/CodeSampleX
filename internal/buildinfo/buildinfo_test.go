package buildinfo

import (
	"testing"
	"time"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func noVCS() vcs { return vcs{} }

// The production image is built from a context that excludes .git, so the
// toolchain stamps nothing and the build args are the only source. Losing
// them silently would put "dev" on the production footer.
func TestResolveUsesTheDeployStamps(t *testing.T) {
	got := Resolve(env(map[string]string{
		CSXVersionEnv:  "2a6af6a8d73f51e4c941908f76527bd9899437ce",
		VersionEnv:     "v0.1.44-66-g2a6af6a",
		BuiltAtEnv:     "2026-08-26T00:11:02Z",
		EnvironmentEnv: "production",
	}), noVCS)

	if got.Version != "v0.1.44-66" {
		t.Errorf("Version = %q, want v0.1.44-66", got.Version)
	}
	if got.Revision != "2a6af6a8d73f51e4c941908f76527bd9899437ce" {
		t.Errorf("Revision = %q", got.Revision)
	}
	if got.ShortRevision() != "2a6af6a" {
		t.Errorf("ShortRevision = %q", got.ShortRevision())
	}
	if got.Environment != "production" {
		t.Errorf("Environment = %q", got.Environment)
	}
	if !got.BuiltAt.Equal(time.Date(2026, 8, 26, 0, 11, 2, 0, time.UTC)) {
		t.Errorf("BuiltAt = %v", got.BuiltAt)
	}
	if !got.Known() {
		t.Error("a fully stamped build reports itself unknown")
	}
}

// CSX_VERSION carries the immutable deployment revision: the deploy
// transaction, the OCI image label and the evidence collector all compare
// against it. Reading it as a display version is what put forty characters
// in the footer.
func TestCSXVersionIsARevisionNotAVersion(t *testing.T) {
	got := Resolve(env(map[string]string{
		CSXVersionEnv: "2a6af6a8d73f51e4c941908f76527bd9899437ce",
	}), noVCS)
	if got.Revision != "2a6af6a8d73f51e4c941908f76527bd9899437ce" {
		t.Errorf("Revision = %q", got.Revision)
	}
	if got.Version == "2a6af6a8d73f51e4c941908f76527bd9899437ce" {
		t.Error("the full revision leaked into Version")
	}
}

// A CSX_VERSION that is not a commit is somebody naming a version, which is
// what the old serverVersion() returned to the admin dashboard.
func TestNonRevisionCSXVersionStaysAVersion(t *testing.T) {
	got := Resolve(env(map[string]string{CSXVersionEnv: "v9.9.9"}), noVCS)
	if got.Version != "v9.9.9" {
		t.Errorf("Version = %q, want v9.9.9", got.Version)
	}
	if got.Revision != "" {
		t.Errorf("Revision = %q, want empty", got.Revision)
	}
}

func TestBuildVersionWinsOverCSXVersion(t *testing.T) {
	got := Resolve(env(map[string]string{
		CSXVersionEnv: "v9.9.9",
		VersionEnv:    "v0.1.44",
	}), noVCS)
	if got.Version != "v0.1.44" {
		t.Errorf("Version = %q, want v0.1.44", got.Version)
	}
}

func TestToolchainStampIsTheFallback(t *testing.T) {
	got := Resolve(env(nil), func() vcs {
		return vcs{
			Revision:    "1111111111111111111111111111111111111111",
			Time:        time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			MainVersion: "v0.9.0",
		}
	})
	if got.Revision != "1111111111111111111111111111111111111111" || got.Version != "v0.9.0" {
		t.Errorf("got %+v", got)
	}
	if !got.BuiltAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("BuiltAt = %v", got.BuiltAt)
	}
}

func TestUnstampedBuildIsUnknownAndNotProduction(t *testing.T) {
	got := Resolve(env(nil), noVCS)
	if got.Known() {
		t.Errorf("an unstamped build claims an identity: %+v", got)
	}
	if got.Environment != EnvDevelopment {
		t.Errorf("Environment = %q, want %q", got.Environment, EnvDevelopment)
	}
	if got.ShortRevision() != "" {
		t.Errorf("ShortRevision = %q, want empty", got.ShortRevision())
	}
}

// "(devel)" is what the toolchain reports for a build with no module
// version. Rendering it is worse than rendering nothing.
func TestDevelMainVersionIsNotAVersion(t *testing.T) {
	got := Resolve(env(nil), func() vcs { return vcs{MainVersion: "(devel)"} })
	if got.Version != "" {
		t.Errorf("Version = %q, want empty", got.Version)
	}
}

func TestNormalizeVersionKeepsAnExactTag(t *testing.T) {
	got := Resolve(env(map[string]string{VersionEnv: "v0.1.44"}), noVCS)
	if got.Version != "v0.1.44" {
		t.Errorf("Version = %q", got.Version)
	}
}

// git describe on a repository with no tags is just the abbreviated commit;
// stripping it would leave nothing at all.
func TestNormalizeVersionKeepsABareAbbreviation(t *testing.T) {
	got := Resolve(env(map[string]string{VersionEnv: "2a6af6a"}), noVCS)
	if got.Version != "2a6af6a" {
		t.Errorf("Version = %q", got.Version)
	}
}

func TestEnvironment(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"production", "production"},
		{"  Production  ", "production"},
		{"staging", "staging"},
		{"pr-190.review", "pr-190.review"},
		{"", EnvDevelopment},
		{"staging (EU)", EnvUnknown},
		{"<script>", EnvUnknown},
	} {
		got := Resolve(env(map[string]string{EnvironmentEnv: tc.raw}), noVCS)
		if got.Environment != tc.want {
			t.Errorf("CSX_ENV=%q → %q, want %q", tc.raw, got.Environment, tc.want)
		}
	}
}

func TestMalformedBuiltAtIsNotADate(t *testing.T) {
	got := Resolve(env(map[string]string{BuiltAtEnv: "yesterday"}), noVCS)
	if !got.BuiltAt.IsZero() {
		t.Errorf("BuiltAt = %v, want zero", got.BuiltAt)
	}
}
