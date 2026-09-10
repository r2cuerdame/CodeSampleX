// Package environment builds EnvironmentFingerprints from the host and
// adapter-provided hints. Raw probe output stays local (goal.md §8.2).
package environment

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var versionRe = regexp.MustCompile(`v?(\d+\.\d+(?:\.\d+)?)`)

const executableProbeTimeout = 500 * time.Millisecond

// probeable maps tools we are willing to shell out to for a version.
type probeSpec struct {
	command string
	args    []string
}

func probe(command string, args ...string) probeSpec {
	return probeSpec{command: command, args: args}
}

var probeable = map[string]probeSpec{
	"node":               probe("node", "--version"),
	"npm":                probe("npm", "--version"),
	"pnpm":               probe("pnpm", "--version"),
	"yarn":               probe("yarn", "--version"),
	"python":             probe("python", "--version"),
	"go":                 probe("go", "version"),
	"rustc":              probe("rustc", "--version"),
	"cargo":              probe("cargo", "--version"),
	"tsc":                probe("tsc", "--version"),
	"uv":                 probe("uv", "--version"),
	"pip":                probe("pip", "--version"),
	"bash":               probe("bash", "--version"),
	"busybox":            probe("busybox"),
	"coreutils":          probe("ls", "--version"),
	"git":                probe("git", "--version"),
	"ssh":                probe("ssh", "-V"),
	"scp":                probe("ssh", "-V"),
	"curl":               probe("curl", "--version"),
	"jq":                 probe("jq", "--version"),
	"openssl":            probe("openssl", "version"),
	"tar":                probe("tar", "--version"),
	"grep":               probe("grep", "--version"),
	"sed":                probe("sed", "--version"),
	"findutils":          probe("find", "--version"),
	"docker":             probe("docker", "--version"),
	"docker-compose":     probe("docker", "compose", "version"),
	"kubectl":            probe("kubectl", "version", "--client"),
	"helm":               probe("helm", "version", "--short"),
	"terraform":          probe("terraform", "version"),
	"opentofu":           probe("tofu", "version"),
	"ffmpeg":             probe("ffmpeg", "-version"),
	"ripgrep":            probe("rg", "--version"),
	"gh":                 probe("gh", "--version"),
	"pwsh":               probe("pwsh", "--version"),
	"powershell":         probe("powershell", "-NoProfile", "-Command", "$PSVersionTable.PSVersion.ToString()"),
	"windows-powershell": probe("powershell", "-NoProfile", "-Command", "$PSVersionTable.PSVersion.ToString()"),
	"cmd":                probe("cmd", "/d", "/c", "ver"),
	"maven":              probe("mvn", "--version"),
	"mvn":                probe("mvn", "--version"),
	"gradle":             probe("gradle", "--version"),
	"gem":                probe("gem", "--version"),
	"bundler":            probe("bundle", "--version"),
	"bundle":             probe("bundle", "--version"),
	"composer":           probe("composer", "--version"),
	"mix":                probe("mix", "--version"),
	"dart":               probe("dart", "--version"),
	"bun":                probe("bun", "--version"),
	"deno":               probe("deno", "--version"),
}

// Probe returns the "major.minor(.patch)" version of a known tool, or ""
// if the tool is missing, unknown, or times out.
func Probe(ctx context.Context, tool string) string {
	spec, ok := probeable[tool]
	if !ok {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, spec.command, spec.args...).CombinedOutput()
	if err != nil {
		return ""
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return ""
	}
	return m[1]
}

// ProbeExecutable returns the version of the exact executable that is about
// to run. It intentionally accepts only the same allowlisted, side-effect-free
// probes as Probe. argv is used solely to distinguish `docker compose` from
// the Docker engine command; arbitrary caller arguments are never executed by
// the probe.
func ProbeExecutable(ctx context.Context, executable, tool string, argv []string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	lookup := tool
	if tool == "docker" && len(argv) > 0 && strings.EqualFold(argv[0], "compose") {
		lookup = "docker-compose"
	}
	spec, ok := probeable[lookup]
	if !ok {
		return ""
	}
	// Use the exact resolved command the caller supplied when the probe targets
	// that tool. Alias probes (maven -> mvn, bundler -> bundle, and similar)
	// retain their canonical executable.
	command := spec.command
	if domain.CommandTool([]string{spec.command}) == tool ||
		(tool == "docker" && lookup == "docker-compose") {
		command = executable
	}
	cctx, cancel := context.WithTimeout(ctx, executableProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, command, spec.args...)
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return ""
	}
	return m[1]
}

// Host is the part of the fingerprint that needs no project and no probe:
// the machine this process is running on. It names no ecosystem, because
// nothing here can know one — that is the caller's to supply.
//
// It exists for the observations that have no scan behind them at all. A
// wrapped command run outside any workspace still ran on a real os and a
// real arch, and an environment missing either is one the ingest endpoint
// refuses outright, taking the observation with it.
func Host() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion:   1,
		OS:              runtime.GOOS,
		OSVersionBucket: osVersionBucket(),
		Arch:            archName(),
	}
}

// Collect assembles a fingerprint. Hints use the JSON field names of
// domain.EnvironmentFingerprint ("runtime", "runtimeVersion", ...); a hint
// naming a tool with an empty version triggers a local probe.
func Collect(ctx context.Context, hints map[string]string) domain.EnvironmentFingerprint {
	fp := Host()
	get := func(k string) string { return strings.TrimSpace(hints[k]) }
	fp.Ecosystem = get("ecosystem")
	fp.Runtime = get("runtime")
	fp.RuntimeVersion = get("runtimeVersion")
	fp.Language = get("language")
	fp.LanguageVersion = get("languageVersion")
	fp.PackageManager = get("packageManager")
	fp.PackageManagerVersion = get("packageManagerVersion")
	fp.ModuleSystem = get("moduleSystem")
	fp.Compiler = get("compiler")
	fp.CompilerVersion = get("compilerVersion")
	fp.ExecutionContext = get("executionContext")
	fp.Virtualization = get("virtualization")
	fp.ContainerRuntime = get("containerRuntime")
	fp.Libc = get("libc")
	fp.BrowserFamily = get("browserFamily")
	fp.BrowserMajor = get("browserMajor")
	fp.Engine = get("engine")
	fp.EngineVersion = get("engineVersion")
	if ua := get("userAgent"); ua != "" && fp.BrowserFamily == "" {
		// Normalize locally; the raw UA is discarded here and never stored.
		bc := ParseUserAgent(ua)
		fp.BrowserFamily, fp.BrowserMajor = bc.Family, bc.Major
		fp.Engine, fp.EngineVersion = bc.Engine, bc.EngineVersion
		if bc.Family != "" && fp.ExecutionContext == "" {
			fp.ExecutionContext = "browser"
		}
	}
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

	// Where the toolchain actually ran. A container, a VM and bare metal
	// are different compatibility populations even when os/arch match.
	if fp.Virtualization == "" && fp.ContainerRuntime == "" {
		fp.Virtualization, fp.ContainerRuntime = detectVirtualization()
	}
	if fp.Libc == "" {
		fp.Libc = detectLibc()
	}
	// The glibc VERSION and the distribution name, which the family and the
	// bare version number could not carry between them: "glibc" says
	// nothing about whether a manylinux_2_28 wheel loads, and "22" says
	// nothing about which distribution numbered it.
	if fp.LibcVersion == "" && fp.Libc == "glibc" {
		fp.LibcVersion = detectLibcVersion()
	}
	if fp.Distro == "" {
		fp.Distro = detectDistro()
	}
	// Every other dimension takes an explicit hint before falling back to
	// detection; this one never read its own. The guard below could not
	// fire, so a caller who stated ci was ignored — harmless today, because
	// detection agrees in the ordinary case, and wrong the moment it does
	// not.
	if v := get("ci"); v != "" {
		fp.CI = v == "true" || v == "1"
	}
	if !fp.CI {
		fp.CI = DetectCI()
	}
	return fp.Normalize()
}

// ciEnvVars are the conventional markers automated runners set. CI fleets
// are clones, so evidence from them must not be mistaken for many
// independent developer environments (goal.md §16.5).
var ciEnvVars = []string{
	"CI", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI",
	"JENKINS_URL", "TF_BUILD", "TEAMCITY_VERSION", "APPVEYOR", "DRONE",
}

// DetectCI reports whether this process looks like an automated runner.
func DetectCI() bool {
	for _, k := range ciEnvVars {
		v := strings.TrimSpace(os.Getenv(k))
		if v == "" || strings.EqualFold(v, "false") || v == "0" {
			continue
		}
		return true
	}
	return false
}

func archName() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	default:
		return runtime.GOARCH
	}
}
