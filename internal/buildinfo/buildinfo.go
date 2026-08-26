// Package buildinfo answers one operational question: which build of this
// server is the process actually running.
//
// Nothing here may be typed by hand into a page or a template. The values
// come from stamps the deployment put on the artifact it built, so a
// redeploy moves them, a rollback moves them back, and a build nobody
// stamped says so instead of guessing.
package buildinfo

import (
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

// Environment names. An unstamped process is development, never production:
// the absence of a deliberate production stamp must not be readable as one.
const (
	EnvDevelopment = "development"
	// EnvUnknown is for a stamp that exists but is not a name this server is
	// willing to render. Reporting "unknown" is honest; silently reporting
	// development would claim something the operator did not say.
	EnvUnknown = "unknown"
)

// Environment variables carrying the stamps. CSXVersionEnv predates the
// others and holds the immutable deployment revision: the production deploy
// transaction, the OCI image label and the evidence collector all compare
// against it, so its meaning is fixed and the newer names are additions.
const (
	CSXVersionEnv  = "CSX_VERSION"
	VersionEnv     = "CSX_BUILD_VERSION"
	BuiltAtEnv     = "CSX_BUILT_AT"
	EnvironmentEnv = "CSX_ENV"
)

// Info is one build's identity.
type Info struct {
	// Version is the human-readable build version, e.g. "v0.1.44-66": the
	// release line this build sits on plus how far past it. Empty only when
	// nothing stamped it.
	Version string
	// Revision is the full 40-character commit the artifact was built from.
	// The full value stays here; only ShortRevision is for display.
	Revision string
	// Environment is the deployment this process belongs to.
	Environment string
	// BuiltAt is when the artifact was built; zero when unstamped.
	BuiltAt time.Time
}

// shortRevisionLen matches what git abbreviates to and what a human compares
// against a commit list.
const shortRevisionLen = 7

// ShortRevision is the display form of Revision, empty when there is none.
func (i Info) ShortRevision() string {
	if len(i.Revision) < shortRevisionLen {
		return i.Revision
	}
	return i.Revision[:shortRevisionLen]
}

// Known reports whether this build carries any identity worth showing. A
// process with neither a version nor a revision has nothing to say, and a
// footer that says nothing is better than one that says "dev" on a host a
// reader believes is production.
func (i Info) Known() bool { return i.Version != "" || i.Revision != "" }

// fullRevision is a git commit as the deploy pins it: lowercase and complete.
var fullRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)

// describeCommitSuffix is the "-g<abbrev>" tail `git describe` appends. The
// commit is rendered beside the version, so carrying it inside the version
// too prints the same fact twice.
var describeCommitSuffix = regexp.MustCompile(`-g[0-9a-f]{7,40}$`)

// bareRevision is what `git describe --tags --always` returns when no tag is
// reachable. When the full revision is stamped alongside it, presenting the
// abbreviation as a version repeats the same identity twice.
var bareRevision = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// environmentName is the shape an environment stamp must have to be shown:
// one lowercase token, not a sentence.
var environmentName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// FromEnvironment resolves the running build from this process's environment.
func FromEnvironment() Info { return Resolve(os.Getenv, readVCS) }

// vcs is what the Go toolchain stamped into the binary. It is a fallback,
// not the source of truth: the production image is built from a context with
// no .git directory, so these are empty exactly where it matters most.
type vcs struct {
	Revision    string
	Time        time.Time
	MainVersion string
}

// Resolve builds the identity from getenv and the toolchain stamp. Both are
// parameters so the resolution order is testable without a real build.
func Resolve(getenv func(string) string, stamp func() vcs) Info {
	build := stamp()
	info := Info{}

	if revision := strings.TrimSpace(getenv(CSXVersionEnv)); fullRevision.MatchString(revision) {
		info.Revision = revision
	} else if fullRevision.MatchString(build.Revision) {
		info.Revision = build.Revision
	}

	switch {
	case strings.TrimSpace(getenv(VersionEnv)) != "":
		info.Version = normalizeVersion(strings.TrimSpace(getenv(VersionEnv)), info.Revision)
	case !fullRevision.MatchString(strings.TrimSpace(getenv(CSXVersionEnv))) && strings.TrimSpace(getenv(CSXVersionEnv)) != "":
		// A CSX_VERSION that is not a revision is somebody naming a version.
		info.Version = normalizeVersion(strings.TrimSpace(getenv(CSXVersionEnv)), info.Revision)
	case build.MainVersion != "" && build.MainVersion != "(devel)":
		info.Version = normalizeVersion(build.MainVersion, info.Revision)
	}

	info.Environment = normalizeEnvironment(getenv(EnvironmentEnv))

	if at, err := time.Parse(time.RFC3339, strings.TrimSpace(getenv(BuiltAtEnv))); err == nil {
		info.BuiltAt = at.UTC()
	} else if !build.Time.IsZero() {
		info.BuiltAt = build.Time.UTC()
	}
	return info
}

// normalizeVersion turns `git describe --tags --always` output into the form
// shown beside a commit.
func normalizeVersion(v, revision string) string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "dev" || trimmed == "(devel)" {
		return ""
	}
	trimmed = describeCommitSuffix.ReplaceAllString(trimmed, "")
	if trimmed == "" {
		return strings.TrimSpace(v)
	}
	if revision != "" && bareRevision.MatchString(trimmed) && strings.HasPrefix(revision, trimmed) {
		return ""
	}
	return trimmed
}

func normalizeEnvironment(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case name == "":
		return EnvDevelopment
	case environmentName.MatchString(name):
		return name
	default:
		return EnvUnknown
	}
}

func readVCS() vcs {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return vcs{}
	}
	out := vcs{MainVersion: bi.Main.Version}
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "vcs.revision":
			out.Revision = setting.Value
		case "vcs.time":
			if at, err := time.Parse(time.RFC3339, setting.Value); err == nil {
				out.Time = at
			}
		}
	}
	return out
}
