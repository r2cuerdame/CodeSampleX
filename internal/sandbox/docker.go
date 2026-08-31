package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// DockerRunner implements the CONTAINER_RUN pipeline (plan C13): the
// resolve stage runs in a network-enabled container, build and contract
// reuse the same bind-mounted workdir with --network=none. The mounted
// dir is always an unpacked sample in a temp workspace — never the host
// project. No host environment is passed into any container.
type DockerRunner struct {
	// ContainerOS is the operating system this Docker daemon runs
	// containers as: "linux" (the default when empty) or "windows".
	//
	// A daemon serves one mode at a time — Docker Desktop switches between
	// Linux and Windows containers and cannot host both — so this is a
	// property of the machine, not of a job. It selects the image family,
	// the workspace path inside the container, and the OS the receipt
	// reports. Without it a Windows worker ran Linux containers and the
	// compatibility map had no Windows verification in it at all.
	ContainerOS string
}

// ContainerOSLinux and ContainerOSWindows are the two modes a Docker
// daemon can serve.
const (
	ContainerOSLinux   = "linux"
	ContainerOSWindows = "windows"
)

func containerOSOrLinux(os string) string {
	if os == ContainerOSWindows {
		return ContainerOSWindows
	}
	return ContainerOSLinux
}

func (r DockerRunner) containerOS() string { return containerOSOrLinux(r.ContainerOS) }

// DetectContainerOS asks the daemon which kind of container it runs.
// Anything unreadable is reported as Linux, the historical assumption.
func DetectContainerOS(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Os}}").Output()
	if err != nil {
		return ContainerOSLinux
	}
	return containerOSOrLinux(strings.ToLower(strings.TrimSpace(string(out))))
}

// SupportsWindows reports whether an ecosystem can be verified on a Windows
// container daemon at all.
//
// It answers from windowsImageFor rather than a second list, because a list
// that drifts from the images is worse than no list: the server would hand a
// Windows worker npm work it can never start.
func SupportsWindows(ecosystem string) bool {
	_, err := windowsImageFor(ecosystem, "", "")
	return err == nil
}

// windowsImageFor selects the verifier image for a Windows container
// daemon.
//
// Only ecosystems with an official Windows base image are offered. Node
// publishes none — the Docker Official Image is Linux-only — so npm work
// is left to Linux workers rather than verified against an image this
// project would have to build and pin itself. A refusal here means the
// worker skips the job and keeps scanning; it never means a wrong image.
func windowsImageFor(ecosystem, runtime, runtimeVersion string) (string, error) {
	switch ecosystem {
	case "golang":
		if runtime != "" && runtime != "go" {
			return "", fmt.Errorf("sandbox: no Windows verifier image for golang runtime %q", runtime)
		}
		return pinned("golang:1.26-windowsservercore-ltsc2022"), nil
	case "pypi":
		if runtime != "" && runtime != "python" {
			return "", fmt.Errorf("sandbox: no Windows verifier image for pypi runtime %q", runtime)
		}
		switch runtimeVersion {
		case "", "3.12":
			return pinned("python:3.12-windowsservercore-ltsc2022"), nil
		case "3.14":
			return pinned("python:3.14-windowsservercore-ltsc2022"), nil
		default:
			return "", fmt.Errorf("sandbox: no Windows verifier image for python runtime version %q", runtimeVersion)
		}
	}
	return "", fmt.Errorf("sandbox: no Windows verifier image for ecosystem %q", ecosystem)
}

// chrome134Executable is the browser the pinned image owns. Exporting the
// path lets Puppeteer releases whose preferred browser revision differs from
// 134 exercise their API against the browser environment the manifest
// requested, instead of failing before launch while looking for their old
// cache key.
const chrome134Executable = "/home/pptruser/.cache/puppeteer/chrome/linux-134.0.6998.35/chrome-linux64/chrome"

// The images these names stand for live in the registry (images.go), which
// is the only place a digest is written down. Resolving them here rather
// than repeating a literal is what keeps the base/libc facts and the
// selectors describing the same bytes.
//
// chrome134Image is the measured browser verifier for Puppeteer 24.4.0: Node
// 22.14.0 and Chrome for Testing 134.0.6998.35. It was run under the same
// 512 MiB / 256 PID / network-off contract as stages before being admitted;
// a plain node image cannot produce browser execution evidence merely
// because the package controlling it is Puppeteer.
var (
	chrome134Image  = pinned("ghcr.io/puppeteer/puppeteer:24.4.0")
	mavenJavaImage  = pinned("maven:3.9.11-eclipse-temurin-21-alpine")
	gradleJavaImage = pinned("gradle:8.14.3-jdk21-alpine")
	python314Image  = pinned("python:3.14-slim")
)

// javaVerifierImage is one line of the Java matrix. The base, distro and
// libc are NOT repeated here: they belong to the image, and the registry in
// images.go states them once beside the digest they describe.
type javaVerifierImage struct {
	image                 string
	runtimeVersion        string
	packageManagerVersion string
}

// An omitted Maven runtimeVersion is the compatibility lane that existed
// before the JDK matrix. Keep its exact Alpine/Temurin image and coarse
// package-manager receipt unchanged. Every explicit matrix line uses the same
// Maven release, JDK vendor, distribution and libc so Java is the only moving
// toolchain dimension.
var mavenJavaImages = map[string]javaVerifierImage{
	"":   {mavenJavaImage, "21", "3.9"},
	"8":  {pinned("maven:3.9.11-amazoncorretto-8-al2023"), "8", "3.9.11"},
	"11": {pinned("maven:3.9.11-amazoncorretto-11-al2023"), "11", "3.9.11"},
	"17": {pinned("maven:3.9.11-amazoncorretto-17-al2023"), "17", "3.9.11"},
	"21": {pinned("maven:3.9.11-amazoncorretto-21-al2023"), "21", "3.9.11"},
	"25": {pinned("maven:3.9.11-amazoncorretto-25-al2023"), "25", "3.9.11"},
}

// Gradle 8.14.3 can run on Java 8 through 24. Java 25 requires Gradle 9.1+
// and therefore necessarily changes two recorded axes; the receipt records
// 9.7.0 rather than pretending that result is a pure-JDK comparison.
var gradleJavaImages = map[string]javaVerifierImage{
	"8":  {pinned("gradle:8.14.3-jdk8-corretto-al2023"), "8", "8.14.3"},
	"11": {pinned("gradle:8.14.3-jdk11-corretto-al2023"), "11", "8.14.3"},
	"17": {pinned("gradle:8.14.3-jdk17-corretto-al2023"), "17", "8.14.3"},
	"21": {pinned("gradle:8.14.3-jdk21-corretto-al2023"), "21", "8.14.3"},
	"25": {pinned("gradle:9.7.0-jdk25-corretto-al2023"), "25", "9.7.0"},
}

func javaImageForManifest(m domain.SampleManifest) (javaVerifierImage, bool, error) {
	env := m.Environment.Normalize()
	if env.Ecosystem != "maven" {
		return javaVerifierImage{}, false, nil
	}
	if env.Runtime != "" && env.Runtime != "java" {
		return javaVerifierImage{}, true, fmt.Errorf("sandbox: Java verifier requires runtime java, got %q", env.Runtime)
	}
	if env.ExecutionContext != "" && env.ExecutionContext != "java" {
		return javaVerifierImage{}, true, fmt.Errorf("sandbox: no Java verifier image for execution context %q", env.ExecutionContext)
	}
	if env.BrowserFamily != "" || env.BrowserMajor != "" || env.Engine != "" || env.EngineVersion != "" {
		return javaVerifierImage{}, true, fmt.Errorf("sandbox: Java verifier does not provide a browser environment")
	}

	var images map[string]javaVerifierImage
	switch m.VerifierAdapter {
	case "maven-java@1", "":
		images = mavenJavaImages
	case "gradle-java@1":
		images = gradleJavaImages
	default:
		return javaVerifierImage{}, true, fmt.Errorf("sandbox: unsupported Maven verifier adapter %q", m.VerifierAdapter)
	}
	spec, ok := images[env.RuntimeVersion]
	if !ok {
		return javaVerifierImage{}, true, fmt.Errorf("sandbox: no %s verifier image for Java runtime version %q", m.VerifierAdapter, env.RuntimeVersion)
	}
	if env.LanguageVersion != "" {
		language, ok := javaReleaseNumber(env.LanguageVersion)
		if !ok {
			return javaVerifierImage{}, true, fmt.Errorf("sandbox: unsupported Java language version %q", env.LanguageVersion)
		}
		runtime, _ := javaReleaseNumber(spec.runtimeVersion)
		if language > runtime {
			return javaVerifierImage{}, true, fmt.Errorf("sandbox: Java language version %q exceeds runtime version %q", env.LanguageVersion, spec.runtimeVersion)
		}
	}
	return spec, true, nil
}

func javaReleaseNumber(version string) (int, bool) {
	switch version {
	case "8":
		return 8, true
	case "11":
		return 11, true
	case "17":
		return 17, true
	case "21":
		return 21, true
	case "25":
		return 25, true
	default:
		return 0, false
	}
}

// imageFor maps an ecosystem AND runtime to its pinned verifier image.
//
// The runtime matters because an ecosystem is not a runtime: npm packages
// run on Node, on Bun and on Deno, and "does this package work on Bun" is
// exactly the compatibility question this project exists to answer. Keying
// the image on ecosystem alone verified every npm sample under Node and
// silently made the execution-context axis (docs/execution-context.md)
// unusable — every sample in the network claimed executionContext "node"
// because no other one could be produced.
func imageFor(ecosystem, runtime string) (string, error) {
	return imageForRuntimeVersion(ecosystem, runtime, "", "")
}

// imageForRuntimeVersion selects the runtime line promised by a manifest.
// Python 3.12 remains the compatibility default for old manifests; 3.14 is
// opt-in and immutable. Other Python lines are refused instead of silently
// producing a receipt for a different interpreter.
func imageForRuntimeVersion(ecosystem, runtime, runtimeVersion, libc string) (string, error) {
	switch ecosystem {
	case "npm":
		switch runtime {
		case "", "node":
			// A manifest that declares glibc gets glibc. Native modules are
			// linked against one libc and do not load on the other, which is
			// what EnvironmentFingerprint.Libc exists to record; running such
			// a sample on musl produces a contract verdict about the verifier.
			// An undeclared libc keeps the historical default, so nothing that
			// was verifiable yesterday moves lanes today.
			if libc == "glibc" {
				return pinned("node:22"), nil
			}
			return pinned("node:22-alpine"), nil
		case "bun":
			return pinned("oven/bun:1-alpine"), nil
		case "deno":
			return pinned("denoland/deno:alpine"), nil
		}
		return "", fmt.Errorf("sandbox: no verifier image for npm runtime %q", runtime)
	case "pypi":
		if runtime != "" && runtime != "python" {
			return "", fmt.Errorf("sandbox: no verifier image for pypi runtime %q", runtime)
		}
		// The Alpine Python entries were kept "and no longer selected" when
		// the lane moved to Debian, because 510 published receipts name them.
		// A manifest that asks for musl is not the default coming back: it is
		// a sample stating the environment it is about, and refusing it would
		// leave 18 samples with no lane at all.
		if libc == "musl" {
			switch runtimeVersion {
			case "", "3.12":
				return pinned("python:3.12-alpine"), nil
			case "3.14":
				return pinned("python:3.14-alpine"), nil
			}
			return "", fmt.Errorf("sandbox: no musl verifier image for python runtime version %q", runtimeVersion)
		}
		switch runtimeVersion {
		case "", "3.12":
			return pinned("python:3.12-slim"), nil
		case "3.14":
			return python314Image, nil
		default:
			return "", fmt.Errorf("sandbox: no verifier image for python runtime version %q", runtimeVersion)
		}
	case "golang":
		// Same rule as npm: a declared libc is honoured, an undeclared one
		// keeps the historical default so nothing moves lanes on its own.
		if libc == "glibc" {
			return pinned("golang:1.26"), nil
		}
		return pinned("golang:1.26-alpine"), nil
	case "cargo":
		return pinned("rust:1-alpine"), nil
	case "composer":
		// One image for both stages on purpose. Composer writes
		// vendor/composer/platform_check.php from the PHP version it
		// resolved under, and the contract aborts when the runtime differs —
		// so resolving on composer:2 and testing on php:8-alpine couples two
		// tags that drift independently.
		return pinned("composer:2"), nil
	case "gem":
		// Debian, not alpine, and the difference is the whole ecosystem.
		//
		// Rubygems has no binary-wheel equivalent: a gem with a C extension
		// compiles at install time, every time. alpine ships no compiler, so
		// faraday -> json 2.21.2 died with "An error occurred while
		// installing json" and took every sample whose dependency tree
		// touches a native gem with it — json, nokogiri, ffi, bcrypt,
		// sqlite3, msgpack. Measured both ways on the same Gemfile:
		// ruby:3-alpine exits 5, ruby:3 installs it and exits 0.
		//
		// This is NOT a general alpine problem, which is why only this one
		// moved. Python was checked the same way and does not have it —
		// pydantic-core installs on python:3.12-alpine because PyPI
		// publishes musllinux wheels, so there is nothing to compile.
		//
		// The cost is honest and recorded: the environment fingerprint now
		// says glibc for ruby rather than musl. glibc is what most ruby
		// runs on, and musl belongs as a second axis of the matrix rather
		// than as the only one anything is ever verified against.
		return pinned("ruby:3"), nil
	case "pub":
		return pinned("dart:3.13.0"), nil
	case "hex":
		return pinned("elixir:1.20.1-alpine"), nil
	case "maven":
		m := domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "maven", Runtime: runtime, RuntimeVersion: runtimeVersion,
		}, VerifierAdapter: "maven-java@1"}
		spec, _, err := javaImageForManifest(m)
		return spec.image, err
	}
	return "", fmt.Errorf("sandbox: no verifier image for ecosystem %q", ecosystem)
}

// imageForManifest selects the stage image from the execution environment,
// not only the package ecosystem. A browser contract still uses Node as its
// harness runtime, but its assertions execute in Chrome and need a real,
// pinned browser image.
func imageForManifest(m domain.SampleManifest) (string, error) {
	return imageForManifestOn(ContainerOSLinux, m)
}

// imageForManifestOn is imageForManifest for a daemon serving a given
// container OS.
func imageForManifestOn(containerOS string, m domain.SampleManifest) (string, error) {
	if containerOSOrLinux(containerOS) == ContainerOSWindows {
		env := m.Environment.Normalize()
		if env.ExecutionContext == "browser" || env.BrowserFamily != "" {
			return "", fmt.Errorf("sandbox: no Windows browser verifier image")
		}
		if _, java, _ := javaImageForManifest(m); java {
			return "", fmt.Errorf("sandbox: no Windows Java verifier image")
		}
		return windowsImageFor(env.Ecosystem, runtimeOf(m, domain.EnvironmentFingerprint{}), env.RuntimeVersion)
	}
	return imageForManifestLinux(m)
}

func imageForManifestLinux(m domain.SampleManifest) (string, error) {
	env := m.Environment.Normalize()
	if spec, java, err := javaImageForManifest(m); java {
		if err != nil {
			return "", err
		}
		return spec.image, nil
	}
	if env.ExecutionContext != "browser" {
		if env.BrowserFamily != "" || env.BrowserMajor != "" || env.Engine != "" || env.EngineVersion != "" {
			return "", fmt.Errorf("sandbox: browser dimensions require browser execution context")
		}
		img, err := imageForRuntimeVersion(env.Ecosystem, env.Runtime, env.RuntimeVersion, env.Libc)
		if err != nil {
			return "", err
		}
		// The image must provide the libc the manifest asked for, or this
		// refuses. Substituting silently writes a receipt saying a sample was
		// verified in an environment it was not verified in, which is the one
		// claim this project cannot make -- the same rule the Python runtime
		// lines already follow. The registry records each image's real libc,
		// established by running it, so this is a lookup and not a guess.
		if env.Libc != "" {
			if entry, ok := registryEntryFor(img); ok && entry.libc != "" && entry.libc != env.Libc {
				return "", fmt.Errorf("sandbox: verifier image %s provides libc %q and cannot satisfy %q",
					entry.alias, entry.libc, env.Libc)
			}
		}
		rt, providedVersion, _ := imageRuntimeForVersion(env.Ecosystem, env.Runtime, env.RuntimeVersion)
		if env.RuntimeVersion != "" && env.Ecosystem != "pypi" &&
			!runtimeVersionMatches(providedVersion, env.RuntimeVersion) {
			return "", fmt.Errorf("sandbox: verifier runtime version %q cannot satisfy %q", providedVersion, env.RuntimeVersion)
		}
		if env.ExecutionContext != "" && env.ExecutionContext != rt {
			return "", fmt.Errorf("sandbox: no verifier image for execution context %q", env.ExecutionContext)
		}
		return img, nil
	}
	if env.Ecosystem != "npm" || (env.Runtime != "" && env.Runtime != "node") {
		return "", fmt.Errorf("sandbox: browser verifier requires npm with node runtime")
	}
	if env.BrowserFamily != "chrome" || env.BrowserMajor != "134" {
		return "", fmt.Errorf("sandbox: no verifier image for browser %q major %q", env.BrowserFamily, env.BrowserMajor)
	}
	if env.Engine != "" && env.Engine != "chromium" {
		return "", fmt.Errorf("sandbox: Chrome verifier cannot provide engine %q", env.Engine)
	}
	if env.EngineVersion != "" && env.EngineVersion != "134" {
		return "", fmt.Errorf("sandbox: Chrome 134 verifier cannot provide engine version %q", env.EngineVersion)
	}
	return chrome134Image, nil
}

// ContainerSupports reports whether this binary has a pinned image for the
// requested ecosystem/runtime. Workers use the same decision as the runner
// before claiming work, so an unsupported job is never taken and failed just
// because it happened to be first in the queue.
func ContainerSupports(ecosystem, runtime string) bool {
	_, err := imageFor(ecosystem, runtime)
	return err == nil
}

// ContainerSupportsRequirements is the exact pre-claim decision used by a
// public worker. Browser fields are part of the closed requirement rather
// than an optimistic hint: a worker with Docker but no pinned Firefox image
// must skip Firefox work and continue scanning the queue.
func ContainerSupportsRequirements(w domain.WorkerRequirements) bool {
	return ContainerSupportsRequirementsOn(ContainerOSLinux, w)
}

// ContainerSupportsRequirementsOn is the same decision for a daemon
// serving a given container OS. A Windows worker must skip work it has no
// image for and keep scanning, exactly as a worker without a pinned
// Firefox image does.
func ContainerSupportsRequirementsOn(containerOS string, w domain.WorkerRequirements) bool {
	// A job that names an OS is work for a daemon serving that OS, whatever
	// else it asks for. The queue filters offers the same way; this is the
	// pre-claim decision agreeing with it.
	if w.OS != "" && !strings.EqualFold(w.OS, containerOSOrLinux(containerOS)) {
		return false
	}
	if w.Ecosystem == "" {
		if containerOSOrLinux(containerOS) == ContainerOSWindows {
			// A requirement-free job is a legacy cross job that could be
			// anything; a Windows daemon has images for two ecosystems and
			// must not gamble on which one it is.
			return false
		}
		return w.Runtime == "" && w.RuntimeVersion == "" && w.ExecutionContext == "" && w.BrowserFamily == "" &&
			w.BrowserMajor == "" && w.Engine == "" && w.EngineVersion == ""
	}
	m := domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
		SchemaVersion: 1,
		Ecosystem:     w.Ecosystem, Runtime: w.Runtime,
		RuntimeVersion:   w.RuntimeVersion,
		ExecutionContext: w.ExecutionContext,
		BrowserFamily:    w.BrowserFamily, BrowserMajor: w.BrowserMajor,
		Engine: w.Engine, EngineVersion: w.EngineVersion,
	}, VerifierAdapter: w.VerifierAdapter}
	_, err := imageForManifestOn(containerOS, m)
	return err == nil
}

// Runtime requirements outside Python historically name either the image's
// recorded line ("22") or a more specific point on it ("22.18"). Preserve
// that behavior while rejecting a genuinely different line such as Node 20.
func runtimeVersionMatches(provided, requested string) bool {
	return requested == provided || strings.HasPrefix(requested, provided+".")
}

// imageRuntime reports the runtime the pinned image provides and its major
// version. Kept beside imageFor so the two never drift.
func imageRuntime(ecosystem, runtime string) (rt, version, language string) {
	return imageRuntimeForVersion(ecosystem, runtime, "")
}

func imageRuntimeForVersion(ecosystem, runtime, runtimeVersion string) (rt, version, language string) {
	switch ecosystem {
	case "npm":
		switch runtime {
		case "bun":
			return "bun", "1", "javascript"
		case "deno":
			return "deno", "2", "javascript"
		}
		return "node", "22", "javascript"
	case "pypi":
		if runtimeVersion == "3.14" {
			return "python", "3.14", "python"
		}
		return "python", "3.12", "python"
	case "golang":
		return "go", "1.26", "go"
	case "cargo":
		return "rust", "1", "rust"
	case "composer":
		return "php", "8", "php"
	case "gem":
		return "ruby", "3", "ruby"
	case "pub":
		return "dart", "3", "dart"
	case "hex":
		return "elixir", "1", "elixir"
	case "maven":
		if runtimeVersion == "" {
			return "java", "21", "java"
		}
		return "java", runtimeVersion, "java"
	}
	return "", "", ""
}

// StageEnvironment reports the container's environment, not the host's:
// the stages run on linux/amd64 with the image's runtime, and a receipt
// that claimed the host OS would poison the compatibility graph.
//
// The verifier images are all alpine, so results carry musl — the single
// dimension that most often decides whether a package with a native
// module loads at all. Recording these runs as plain "linux" would make
// them look like they proved something about glibc distros too.
func (r DockerRunner) StageEnvironment(host domain.EnvironmentFingerprint, m domain.SampleManifest) domain.EnvironmentFingerprint {
	eco := m.Environment.Ecosystem
	if eco == "" {
		eco = host.Ecosystem
	}
	img, _ := imageForManifestOn(r.containerOS(), m)
	// The sample's declared runtime picks the container, so it must also
	// describe the receipt. Reading the HOST runtime here would stamp a
	// bun contract as node whenever the operator happened to run node.
	rt, ver, lang := imageRuntimeForVersion(eco, runtimeOf(m, host), m.Environment.RuntimeVersion)
	// The base and the libc come from the IMAGE, not from a constant.
	// "the verifier images are all alpine" stopped being true when Dart
	// arrived: dart:3.13.0 is Debian, so every Dart receipt claimed
	// osVersionBucket alpine and libc musl for a run on glibc — and musl
	// versus glibc is the dimension the grader treats as decisive for
	// whether a native module loads at all.
	bucket, libc := imageBase(img)
	// The architecture comes from the HOST. Nothing passes --platform, so
	// the container runs the host's architecture; stamping x64 meant every
	// receipt from an arm64 machine — an Apple Silicon laptop, a Graviton
	// runner — described a run that never happened.
	arch := host.Normalize().Arch
	if arch == "" {
		arch = "x64"
	}
	packageManager := m.Environment.PackageManager
	if eco == "npm" {
		packageManager = NPMResolver(runtimeOf(m, host))
	} else if m.VerifierAdapter == "gradle-java@1" {
		packageManager = "gradle"
	} else if eco == "maven" {
		packageManager = "maven"
	}
	// The receipt describes the container, and on a Windows daemon that
	// container really is Windows: the image family names the base and
	// there is no libc to report.
	osName := r.containerOS()
	if osName == ContainerOSWindows {
		bucket, libc = "windowsservercore", ""
	}
	env := domain.EnvironmentFingerprint{
		SchemaVersion:    1,
		Ecosystem:        eco,
		OS:               osName,
		OSVersionBucket:  bucket,
		Arch:             arch,
		Runtime:          rt,
		RuntimeVersion:   ver,
		Language:         lang,
		ExecutionContext: rt,
		ModuleSystem:     m.Environment.ModuleSystem,
		PackageManager:   packageManager,
		Virtualization:   "container",
		ContainerRuntime: "docker",
		Libc:             libc,
		CI:               host.CI,
	}
	if spec, java, err := javaImageForManifest(m); java && err == nil {
		if entry, ok := registryEntryFor(spec.image); ok {
			env.OSVersionBucket = entry.bucket
			env.Distro = entry.distro
			env.Libc = entry.libc
		}
	}
	if img == chrome134Image {
		env.ExecutionContext = "browser"
		env.BrowserFamily = "chrome"
		env.BrowserMajor = "134"
		env.Engine = "chromium"
		env.EngineVersion = "134"
	}
	if eco == "maven" {
		env.LanguageVersion = m.Environment.LanguageVersion
		if env.LanguageVersion == "" {
			env.LanguageVersion = ver
		}
		env.Compiler = "javac"
		env.CompilerVersion = ver
		if spec, java, err := javaImageForManifest(m); java && err == nil {
			env.PackageManagerVersion = spec.packageManagerVersion
		}
	}
	return env.Normalize()
}

// VerifierImage reports which image bytes this runner will execute for a
// manifest, so the receipt can name them and the signature can cover them.
//
// It is the SAME selection the stages use, not a second description of it:
// a field derived from a parallel table would eventually describe an image
// the stages had stopped running.
func (r DockerRunner) VerifierImage(m domain.SampleManifest) *domain.VerifierImage {
	img, err := imageForManifestOn(r.containerOS(), m)
	if err != nil {
		return nil
	}
	return verifierImageOf(img)
}

// dockerArgs builds the docker run invocation. Resource limits per plan
// C13: --memory=512m --pids-limit=256. The only --env flags are the fixed
// cache paths from stageEnv, all pointing inside the mounted workspace;
// no host environment is ever forwarded.
func dockerArgs(image, dir string, networkOff bool, env, cmd []string, name string) []string {
	return dockerArgsOn(ContainerOSLinux, image, dir, networkOff, env, cmd, name)
}

// dockerArgsOn builds the invocation for a daemon serving containerOS.
// A Windows container mounts at C:\work and takes no --pids-limit: the
// Windows isolation layer does not implement one, and passing it makes
// docker refuse the run outright rather than tighten it.
func dockerArgsOn(containerOS, image, dir string, networkOff bool, env, cmd []string, name string) []string {
	windows := containerOSOrLinux(containerOS) == ContainerOSWindows
	args := []string{"docker", "run", "--rm"}
	if name != "" {
		args = append(args, "--name", name)
	}
	if networkOff {
		args = append(args, "--network=none")
	}
	if windows {
		args = append(args, "--memory=512m")
		for _, e := range env {
			args = append(args, "--env", e)
		}
		args = append(args, "-v", dir+`:C:\work`, "-w", `C:\work`, image)
		return append(args, cmd...)
	}
	if image == chrome134Image || isGradleJavaImage(image) {
		// These official images default to non-root users (Chrome uid 10042,
		// Gradle uid 1000) which cannot write an arbitrary host-owned temporary
		// workspace on Linux. Existing verifier images already run stages as
		// container root. The disposable outer Docker isolation exposes only
		// /work; Chrome uses --no-sandbox and Gradle's fixed cache stays there.
		args = append(args, "--init", "--user", "0:0")
	}
	args = append(args, "--memory=512m", "--pids-limit=256")
	for _, e := range env {
		args = append(args, "--env", e)
	}
	args = append(args, "-v", dir+":/work", "-w", "/work", image)
	return append(args, cmd...)
}

func (r DockerRunner) stage(ctx context.Context, dir string, m domain.SampleManifest, networkOff bool, cmd []string) StageResult {
	img, err := imageForManifestOn(r.containerOS(), m)
	if err != nil {
		return StageResult{Result: ResultFail, Log: err.Error()}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return StageResult{Result: ResultFail, Log: "sandbox: resolve workdir: " + err.Error()}
	}
	// A named container, so a stage that outruns its timeout can be
	// killed rather than left behind.
	//
	// The timeout cancels the context, which kills the `docker run`
	// CLIENT. The container belongs to dockerd and keeps running, and
	// --rm only removes it once it exits — which for a hung build is
	// never. One was found still compiling fifteen minutes into a
	// five-minute stage, on a machine where six containers is the measured
	// ceiling, so every timed-out verification permanently took a share of
	// the pool. Over a night that is not a leak, it is the throughput.
	name := containerName(abs, networkOff, cmd)
	env := stageEnvironmentForImage(img, m.Environment.Ecosystem, m.Environment.Runtime)
	res := runStage(ctx, "", dockerArgsOn(r.containerOS(), img, abs, networkOff, env, cmd, name))
	if ctx.Err() != nil || res.Result == ResultFail {
		reapContainer(name)
	}
	return res
}

func stageEnvironmentForImage(image, ecosystem, runtime string) []string {
	env := stageEnv(ecosystem, runtime)
	if isGradleJavaImage(image) {
		env = []string{
			"GRADLE_USER_HOME=" + vendorDir + "/gradle-home",
			"JAVA_TOOL_OPTIONS=-Dfile.encoding=UTF-8",
		}
	}
	if image == chrome134Image {
		env = append(env,
			"PUPPETEER_CACHE_DIR=/home/pptruser/.cache/puppeteer",
			"PUPPETEER_EXECUTABLE_PATH="+chrome134Executable,
		)
	}
	return env
}

func isGradleJavaImage(image string) bool {
	for _, spec := range gradleJavaImages {
		if spec.image == image {
			return true
		}
	}
	return image == gradleJavaImage
}

// containerName derives a name unique to this stage of this workspace.
//
// The workspace directory is already unique per verification (the caller
// unpacks into a fresh temp dir), and the stage is distinguished by
// whether the network is off and by the command, so two stages of one
// sample never collide.
func containerName(dir string, networkOff bool, cmd []string) string {
	h := sha256.Sum256([]byte(dir + "\x00" + strconv.FormatBool(networkOff) +
		"\x00" + strings.Join(cmd, "\x00")))
	return "csx-" + hex.EncodeToString(h[:8])
}

// reapContainer kills a container that may still be running after its
// stage gave up. Best effort and quick: it runs on the failure path, and a
// verifier that blocks on cleanup is worse than a container that lingers a
// few seconds longer.
//
// A container that already exited makes this a no-op — docker kill on an
// absent name is an error nobody needs to hear about.
func reapContainer(name string) {
	if name == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = execCombined(ctx, "", []string{"docker", "kill", name})
}

// Resolve fetches dependencies with the network ON but lifecycle scripts OFF.
func (r DockerRunner) Resolve(ctx context.Context, dir string, m domain.SampleManifest) StageResult {
	if m.Environment.Ecosystem == "maven" {
		switch m.VerifierAdapter {
		case "maven-java@1":
			if err := prepareMavenResolver(dir, m); err != nil {
				return StageResult{Result: ResultFail, Log: err.Error()}
			}
		case "gradle-java@1":
			if err := prepareGradleResolver(dir, m); err != nil {
				return StageResult{Result: ResultFail, Log: err.Error()}
			}
			return r.stage(ctx, dir, m, false, []string{"sh", "-c", gradleResolveScript})
		default:
			return StageResult{Result: ResultFail, Log: fmt.Sprintf("sandbox: unsupported Maven verifier adapter %q", m.VerifierAdapter)}
		}
	}
	cmd, err := resolveCommand(m.Environment.Ecosystem, m.Environment.Runtime)
	if err != nil {
		return StageResult{Result: ResultFail, Log: err.Error()}
	}
	return r.stage(ctx, dir, m, false, cmd)
}

// Build runs the manifest's build command with the network OFF.
func (r DockerRunner) Build(ctx context.Context, dir string, m domain.SampleManifest) StageResult {
	if m.VerifierAdapter == "gradle-java@1" {
		return r.stage(ctx, dir, m, true, append([]string(nil), gradleBuildCommand...))
	}
	if len(m.BuildCommand) == 0 {
		return skipped("no build command in manifest")
	}
	return r.stage(ctx, dir, m, true, m.BuildCommand)
}

// Contract runs the manifest's contract command with the network OFF.
func (r DockerRunner) Contract(ctx context.Context, dir string, m domain.SampleManifest) StageResult {
	if m.VerifierAdapter == "gradle-java@1" {
		return r.stage(ctx, dir, m, true, append([]string(nil), gradleContractCommand...))
	}
	if len(m.ContractCommand) == 0 {
		return skipped("no contract command in manifest")
	}
	return r.stage(ctx, dir, m, true, m.ContractCommand)
}

// runtimeOf prefers the runtime the sample declares; the host runtime is a
// fallback for manifests that predate the field.
func runtimeOf(m domain.SampleManifest, host domain.EnvironmentFingerprint) string {
	if m.Environment.Runtime != "" {
		return m.Environment.Runtime
	}
	// NOT the host's runtime. The container is chosen from the sample's
	// ecosystem, and the host's runtime is whatever the operator happened
	// to have — so verifying an npm sample on a machine whose collected
	// runtime was "go" picked an image by "go" and then stamped the receipt
	// with it. A receipt that names a runtime the container never had is a
	// false statement about where the contract ran, and every later grade
	// against it inherits the error.
	//
	// Empty means "the ecosystem's default", which is what imageFor and
	// imageRuntime already do with it.
	return ""
}
