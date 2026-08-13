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
		"npm": {"npm", "ci", "--ignore-scripts"},
		"pypi": {"pip", "install", "--no-deps", "--no-compile",
			"--target", "/work/.csx-vendor/py", "-r", "requirements.txt"},
		"golang": {"go", "mod", "download"},
		"cargo":  {"cargo", "fetch"},
	}
	for eco, want := range cases {
		got, err := resolveCommand(eco)
		if err != nil {
			t.Fatalf("%s: %v", eco, err)
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%s: %v, want %v", eco, got, want)
		}
	}
	if _, err := resolveCommand("nuget"); err == nil {
		t.Fatal("unknown ecosystem must error")
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
		img, err := imageFor(eco)
		if err != nil {
			t.Fatalf("%s: %v", eco, err)
		}
		if img != want {
			t.Fatalf("%s: %s, want %s", eco, img, want)
		}
	}
	if _, err := imageFor("nuget"); err == nil {
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

func TestNativeRunner(t *testing.T) {
	var calls [][]string
	var dirs []string
	stubExec(t, func(ctx context.Context, dir string, argv []string) ([]byte, error) {
		calls = append(calls, argv)
		dirs = append(dirs, dir)
		return []byte("ok"), nil
	})

	r := NativeRunner{}
	m := npmManifest()
	dir := t.TempDir()

	if res := r.Resolve(context.Background(), dir, m); res.Result != ResultPass {
		t.Fatalf("resolve: %+v", res)
	}
	if got := strings.Join(calls[0], " "); got != "npm ci --ignore-scripts" {
		t.Fatalf("native resolve command %q", got)
	}
	if dirs[0] != dir {
		t.Fatalf("native resolve ran in %q, want %q", dirs[0], dir)
	}

	if res := r.Build(context.Background(), dir, m); res.Result != ResultPass {
		t.Fatalf("build: %+v", res)
	}

	// Contract NEVER runs natively: honest SKIPPED.
	res := r.Contract(context.Background(), dir, m)
	if res.Result != ResultSkipped {
		t.Fatalf("native contract %s, want SKIPPED", res.Result)
	}
	if len(calls) != 2 {
		t.Fatalf("native contract must not exec anything (calls=%d)", len(calls))
	}

	// No build command → SKIPPED without exec.
	m.BuildCommand = nil
	if res := r.Build(context.Background(), dir, m); res.Result != ResultSkipped {
		t.Fatalf("empty build command → %s, want SKIPPED", res.Result)
	}
	if len(calls) != 2 {
		t.Fatal("empty build command must not exec")
	}
}

// TestStageEnvKeepsCachesInTheWorkspace pins why non-npm verification used
// to be impossible: resolve and contract are separate containers sharing
// only /work, so a toolchain cache anywhere else is gone before the
// contract runs. Every value here must therefore live under /work, and
// none may forward host environment.
func TestStageEnvKeepsCachesInTheWorkspace(t *testing.T) {
	for _, eco := range []string{"pypi", "golang", "cargo"} {
		env := stageEnv(eco)
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
	if env := stageEnv("npm"); len(env) != 0 {
		t.Errorf("npm needs no stage env (node_modules is already in the workspace), got %v", env)
	}
}
