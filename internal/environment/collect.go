// Package environment builds EnvironmentFingerprints from the host and
// adapter-provided hints. Raw probe output stays local (goal.md §8.2).
package environment

import (
	"context"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var versionRe = regexp.MustCompile(`v?(\d+\.\d+(?:\.\d+)?)`)

// probeable maps tools we are willing to shell out to for a version.
var probeable = map[string][]string{
	"node":   {"--version"},
	"npm":    {"--version"},
	"pnpm":   {"--version"},
	"yarn":   {"--version"},
	"python": {"--version"},
	"go":     {"version"},
	"rustc":  {"--version"},
	"cargo":  {"--version"},
	"tsc":    {"--version"},
	"uv":     {"--version"},
	"pip":    {"--version"},
}

// Probe returns the "major.minor(.patch)" version of a known tool, or ""
// if the tool is missing, unknown, or times out.
func Probe(ctx context.Context, tool string) string {
	args, ok := probeable[tool]
	if !ok {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, tool, args...).Output()
	if err != nil {
		return ""
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return ""
	}
	return m[1]
}

// Collect assembles a fingerprint. Hints use the JSON field names of
// domain.EnvironmentFingerprint ("runtime", "runtimeVersion", ...); a hint
// naming a tool with an empty version triggers a local probe.
func Collect(ctx context.Context, hints map[string]string) domain.EnvironmentFingerprint {
	fp := domain.EnvironmentFingerprint{
		SchemaVersion:   1,
		OS:              runtime.GOOS,
		OSVersionBucket: osVersionBucket(),
		Arch:            archName(),
	}
	get := func(k string) string { return strings.TrimSpace(hints[k]) }
	fp.Ecosystem = get("ecosystem")
	fp.Runtime = get("runtime")
	fp.RuntimeVersion = get("runtimeVersion")
	fp.Language = get("language")
	fp.LanguageVersion = get("languageVersion")
	fp.PackageManager = get("packageManager")
	fp.PackageManagerVersion = get("packageManagerVersion")
	fp.ModuleSystem = get("moduleSystem")
	if f := get("frameworks"); f != "" {
		fp.Frameworks = strings.Split(f, ",")
	}
	if fp.Runtime != "" && fp.RuntimeVersion == "" {
		fp.RuntimeVersion = Probe(ctx, fp.Runtime)
	}
	if fp.PackageManager != "" && fp.PackageManagerVersion == "" {
		fp.PackageManagerVersion = Probe(ctx, fp.PackageManager)
	}
	if fp.Language == "typescript" && fp.LanguageVersion == "" {
		fp.LanguageVersion = Probe(ctx, "tsc")
	}
	return fp
}

func archName() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	default:
		return runtime.GOARCH
	}
}
