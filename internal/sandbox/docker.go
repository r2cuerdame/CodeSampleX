package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
type DockerRunner struct{}

// chrome134Image is the measured browser verifier for Puppeteer 24.4.0.
// It contains Node 22.14.0 and Chrome for Testing 134.0.6998.35. The image
// was run under the same 512 MiB / 256 PID / network-off contract as stages
// before being admitted here; a plain node image cannot produce browser
// execution evidence merely because the package controlling it is Puppeteer.
const (
	chrome134Image = "ghcr.io/puppeteer/puppeteer:24.4.0@sha256:ca2087099ad5769b74c89135c663cbb2a76e07d3e261bb3e2da83be98409a68a"
	// The digest-pinned image owns this path. Exporting it lets Puppeteer
	// releases whose preferred browser revision differs from 134 exercise
	// their API against the browser environment the manifest requested,
	// instead of failing before launch while looking for their old cache key.
	chrome134Executable = "/home/pptruser/.cache/puppeteer/chrome/linux-134.0.6998.35/chrome-linux64/chrome"
	mavenJavaImage      = "maven:3.9.11-eclipse-temurin-21-alpine@sha256:922927df2c662cdd47ddb116443d6bec4696cfae3de1a0ddac8fcc7b87ce61ae"
)

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
	switch ecosystem {
	case "npm":
		switch runtime {
		case "", "node":
			return "node:22-alpine", nil
		case "bun":
			return "oven/bun:1-alpine", nil
		case "deno":
			return "denoland/deno:alpine", nil
		}
		return "", fmt.Errorf("sandbox: no verifier image for npm runtime %q", runtime)
	case "pypi":
		return "python:3.12-alpine", nil
	case "golang":
		return "golang:1.26-alpine", nil
	case "cargo":
		return "rust:1-alpine", nil
	case "composer":
		// One image for both stages on purpose. Composer writes
		// vendor/composer/platform_check.php from the PHP version it
		// resolved under, and the contract aborts when the runtime differs —
		// so resolving on composer:2 and testing on php:8-alpine couples two
		// tags that drift independently.
		return "composer:2", nil
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
		return "ruby:3", nil
	case "pub":
		return "dart:3.13.0", nil
	case "hex":
		return "elixir:1.20.1-alpine", nil
	case "maven":
		if runtime != "" && runtime != "java" {
			return "", fmt.Errorf("sandbox: no verifier image for maven runtime %q", runtime)
		}
		return mavenJavaImage, nil
	}
	return "", fmt.Errorf("sandbox: no verifier image for ecosystem %q", ecosystem)
}

// imageForManifest selects the stage image from the execution environment,
// not only the package ecosystem. A browser contract still uses Node as its
// harness runtime, but its assertions execute in Chrome and need a real,
// pinned browser image.
func imageForManifest(m domain.SampleManifest) (string, error) {
	env := m.Environment.Normalize()
	if env.ExecutionContext != "browser" {
		if env.BrowserFamily != "" || env.BrowserMajor != "" || env.Engine != "" || env.EngineVersion != "" {
			return "", fmt.Errorf("sandbox: browser dimensions require browser execution context")
		}
		img, err := imageFor(env.Ecosystem, env.Runtime)
		if err != nil {
			return "", err
		}
		rt, _, _ := imageRuntime(env.Ecosystem, env.Runtime)
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
	if w.Ecosystem == "" {
		return w.Runtime == "" && w.ExecutionContext == "" && w.BrowserFamily == "" &&
			w.BrowserMajor == "" && w.Engine == "" && w.EngineVersion == ""
	}
	m := domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
		SchemaVersion: 1,
		Ecosystem:     w.Ecosystem, Runtime: w.Runtime,
		ExecutionContext: w.ExecutionContext,
		BrowserFamily:    w.BrowserFamily, BrowserMajor: w.BrowserMajor,
		Engine: w.Engine, EngineVersion: w.EngineVersion,
	}}
	_, err := imageForManifest(m)
	return err == nil
}

// imageRuntime reports the runtime the pinned image provides and its major
// version. Kept beside imageFor so the two never drift.
func imageRuntime(ecosystem, runtime string) (rt, version, language string) {
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
		return "java", "21", "java"
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
func (DockerRunner) StageEnvironment(host domain.EnvironmentFingerprint, m domain.SampleManifest) domain.EnvironmentFingerprint {
	eco := m.Environment.Ecosystem
	if eco == "" {
		eco = host.Ecosystem
	}
	img, _ := imageForManifest(m)
	// The sample's declared runtime picks the container, so it must also
	// describe the receipt. Reading the HOST runtime here would stamp a
	// bun contract as node whenever the operator happened to run node.
	rt, ver, lang := imageRuntime(eco, runtimeOf(m, host))
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
	} else if eco == "maven" {
		packageManager = "maven"
	}
	env := domain.EnvironmentFingerprint{
		SchemaVersion:    1,
		Ecosystem:        eco,
		OS:               "linux",
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
	if img == chrome134Image {
		env.ExecutionContext = "browser"
		env.BrowserFamily = "chrome"
		env.BrowserMajor = "134"
		env.Engine = "chromium"
		env.EngineVersion = "134"
	}
	if eco == "maven" {
		env.LanguageVersion = "21"
		env.Compiler = "javac"
		env.CompilerVersion = "21"
		env.PackageManagerVersion = "3.9"
	}
	return env.Normalize()
}

// dockerArgs builds the docker run invocation. Resource limits per plan
// C13: --memory=512m --pids-limit=256. The only --env flags are the fixed
// cache paths from stageEnv, all pointing inside the mounted workspace;
// no host environment is ever forwarded.
func dockerArgs(image, dir string, networkOff bool, env, cmd []string, name string) []string {
	args := []string{"docker", "run", "--rm"}
	if name != "" {
		args = append(args, "--name", name)
	}
	if networkOff {
		args = append(args, "--network=none")
	}
	if image == chrome134Image {
		// The official image defaults to uid 10042, which cannot write a
		// normal user's bind-mounted temporary workspace on Linux. Existing
		// verifier images already run stages as container root. Chrome is
		// launched by the sample with --no-sandbox inside this disposable
		// outer Docker isolation, and the fixed cache points at the browser
		// bundled in the image rather than /root.
		args = append(args, "--init", "--user", "0:0")
	}
	args = append(args, "--memory=512m", "--pids-limit=256")
	for _, e := range env {
		args = append(args, "--env", e)
	}
	args = append(args, "-v", dir+":/work", "-w", "/work", image)
	return append(args, cmd...)
}

func (DockerRunner) stage(ctx context.Context, dir string, m domain.SampleManifest, networkOff bool, cmd []string) StageResult {
	img, err := imageForManifest(m)
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
	res := runStage(ctx, "", dockerArgs(img, abs, networkOff, env, cmd, name))
	if ctx.Err() != nil || res.Result == ResultFail {
		reapContainer(name)
	}
	return res
}

func stageEnvironmentForImage(image, ecosystem, runtime string) []string {
	env := stageEnv(ecosystem, runtime)
	if image == chrome134Image {
		env = append(env,
			"PUPPETEER_CACHE_DIR=/home/pptruser/.cache/puppeteer",
			"PUPPETEER_EXECUTABLE_PATH="+chrome134Executable,
		)
	}
	return env
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
		if err := prepareMavenResolver(dir, m); err != nil {
			return StageResult{Result: ResultFail, Log: err.Error()}
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
	if len(m.BuildCommand) == 0 {
		return skipped("no build command in manifest")
	}
	return r.stage(ctx, dir, m, true, m.BuildCommand)
}

// Contract runs the manifest's contract command with the network OFF.
func (r DockerRunner) Contract(ctx context.Context, dir string, m domain.SampleManifest) StageResult {
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

// imageBases is what each verifier image ACTUALLY is, verified by running
// it. A receipt says which libc the contract ran against, and musl versus
// glibc is the dimension the grader treats as decisive for whether a
// package with a native module loads at all — so this cannot be a guess.
//
// It was inferred from the image NAME: anything containing "alpine" was
// musl, everything else glibc. composer:2 is an Alpine image that does not
// say so in its tag, so every PHP receipt this project has ever produced
// claimed glibc for a run on musl — and a caller on Debian was told a
// musl-verified sample matched their libc exactly.
//
// A name is not evidence. TestImageBaseMatchesTheRealImage runs each of
// these and fails if the table drifts, which is the only thing that keeps
// it true as the image set grows.
var imageBases = map[string]struct{ bucket, libc string }{
	"node:22-alpine":       {"alpine", "musl"},
	chrome134Image:         {"debian", "glibc"},
	"python:3.12-alpine":   {"alpine", "musl"},
	"golang:1.26-alpine":   {"alpine", "musl"},
	"rust:1-alpine":        {"alpine", "musl"},
	"ruby:3-alpine":        {"alpine", "musl"},
	"ruby:3":               {"debian", "glibc"},
	"elixir:1.20.1-alpine": {"alpine", "musl"},
	"denoland/deno:alpine": {"alpine", "musl"},
	"oven/bun:1-alpine":    {"alpine", "musl"},
	"composer:2":           {"alpine", "musl"}, // Alpine, despite the tag
	"dart:3.13.0":          {"debian", "glibc"},
	mavenJavaImage:         {"alpine", "musl"},
}

// imageBase reports the distribution bucket and libc of a verifier image.
//
// An image absent from the table falls back to the name, and to glibc when
// the name says nothing — over-claiming musl is the error that misleads,
// because it is the narrower claim.
func imageBase(image string) (bucket, libc string) {
	if b, ok := imageBases[image]; ok {
		return b.bucket, b.libc
	}
	if strings.Contains(image, "alpine") {
		return "alpine", "musl"
	}
	return "debian", "glibc"
}
