package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// TestDockerSmokeNodeEcho drives the real Docker pipeline against a tiny
// node echo-contract fixture. It only runs with CSX_TEST_DOCKER=1 and a
// working Docker daemon; everything else in this package is unit-tested
// with a stubbed exec.
func TestDockerSmokeNodeEcho(t *testing.T) {
	if os.Getenv("CSX_TEST_DOCKER") != "1" {
		t.Skip("set CSX_TEST_DOCKER=1 to run the real-docker smoke test")
	}
	if Detect(context.Background()) != domain.CapContainerRun {
		t.Skip("docker daemon not available")
	}

	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"name":"csx-echo-sample","version":"1.0.0","private":true}`)
	write("package-lock.json", `{
  "name": "csx-echo-sample",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {"name": "csx-echo-sample", "version": "1.0.0"}
  }
}`)
	write("src/echo.mjs", "export function echo(x) { return x }\n")
	write("test/contract.mjs", `import { echo } from "../src/echo.mjs";
if (echo("csx") !== "csx") { console.error("contract failed"); process.exit(1); }
console.log("contract ok");
`)

	m := domain.SampleManifest{
		SchemaVersion: 1,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22",
		},
		Packages:        []string{"pkg:npm/csx-echo-sample@1.0.0"},
		ContractCommand: []string{"node", "test/contract.mjs"},
		VerifierAdapter: "node-typescript@1",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	r := DockerRunner{}
	if res := r.Resolve(ctx, dir, m); res.Result != ResultPass {
		t.Fatalf("resolve: %s\n%s", res.Result, res.Log)
	}
	if res := r.Build(ctx, dir, m); res.Result != ResultSkipped {
		t.Fatalf("build (no command): %s, want SKIPPED\n%s", res.Result, res.Log)
	}
	if res := r.Contract(ctx, dir, m); res.Result != ResultPass {
		t.Fatalf("contract: %s\n%s", res.Result, res.Log)
	}
}
