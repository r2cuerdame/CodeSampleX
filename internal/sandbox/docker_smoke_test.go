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

// TestDockerSmokeHexResolve drives the real hex resolve against a fixture
// mix.lock. It exists because hexResolveScript is the one resolve step that
// is a script rather than an argv, and a script can be syntactically fine
// and semantically empty: an earlier revision lost its two backreferences
// in transit and ran `mix hex.package fetch "" ""` for every package. That
// failure is invisible to every other test in this package, which stubs
// exec, and invisible to review, because the script still reads correctly.
//
// It asserts on the unpacked tree, not on the exit status, since a resolve
// that fetched nothing at all can still exit 0.
func TestDockerSmokeHexResolve(t *testing.T) {
	if os.Getenv("CSX_TEST_DOCKER") != "1" {
		t.Skip("set CSX_TEST_DOCKER=1 to run the real-docker smoke test")
	}
	if Detect(context.Background()) != domain.CapContainerRun {
		t.Skip("docker daemon not available")
	}

	dir := t.TempDir()
	// A real mix.lock line, verbatim: the script reads this file as text and
	// must never hand it to an Elixir evaluator.
	lock := `%{
  "jason": {:hex, :jason, "1.4.4", "b9226785a9aa77b6857ca22832cffa5d5011a667207eb2a0ad56adb5db443b8a", [:mix], [{:decimal, "~> 1.0 or ~> 2.0", [hex: :decimal, repo: "hexpm", optional: true]}], "hexpm", "c5eb0cab91f094599f94d55bc63409236a8ec69a21a67814529e8d5f6cc90b3b"},
}
`
	if err := os.WriteFile(filepath.Join(dir, "mix.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}

	m := domain.SampleManifest{
		SchemaVersion: 1,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "hex", OS: "linux", Arch: "amd64",
			Runtime: "elixir", RuntimeVersion: "1",
		},
		Packages:        []string{"pkg:hex/jason@1.4.4"},
		ContractCommand: []string{"mix", "test", "--no-deps-check"},
		VerifierAdapter: "hex@1",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if res := (DockerRunner{}).Resolve(ctx, dir, m); res.Result != ResultPass {
		t.Fatalf("resolve: %s\n%s", res.Result, res.Log)
	}
	if _, err := os.Stat(filepath.Join(dir, "deps", "jason", "mix.exs")); err != nil {
		t.Fatalf("jason was not unpacked into deps/: %v", err)
	}
}

// TestDockerSmokeMavenJava proves the narrow Java path end to end: an exact
// Central coordinate is resolved without reading the sample pom/.mvn tree,
// javac sees only the locked JAR directory, and the contract executes after
// Docker networking has been disabled. It is opt-in because it pulls a large
// JDK image and contacts Maven Central during resolve.
func TestDockerSmokeMavenJava(t *testing.T) {
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
	write("pom.xml", `<project><build><plugins><plugin><artifactId>must-not-run</artifactId></plugin></plugins></build></project>`)
	write(".mvn/maven.config", `-s /work/attacker-settings.xml`)
	write("Contract.java", `import org.apache.commons.lang3.StringUtils;
public final class Contract {
  public static void main(String[] args) {
    if (!StringUtils.isBlank(" \t")) throw new AssertionError("contract failed");
    System.out.println("contract ok");
  }
}
`)

	m := mavenManifest("pkg:maven/org.apache.commons/commons-lang3@3.17.0")
	m.BuildCommand = []string{"sh", "-c", "mkdir -p /work/.csx-vendor/classes && javac -cp '/work/.csx-vendor/maven-jars/*' -d /work/.csx-vendor/classes Contract.java"}
	m.ContractCommand = []string{"sh", "-c", "java -cp '/work/.csx-vendor/classes:/work/.csx-vendor/maven-jars/*' Contract"}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	r := DockerRunner{}
	if res := r.Resolve(ctx, dir, m); res.Result != ResultPass {
		t.Fatalf("resolve: %s\n%s", res.Result, res.Log)
	}
	if res := r.Build(ctx, dir, m); res.Result != ResultPass {
		t.Fatalf("build: %s\n%s", res.Result, res.Log)
	}
	if res := r.Contract(ctx, dir, m); res.Result != ResultPass {
		t.Fatalf("contract: %s\n%s", res.Result, res.Log)
	}
	env := r.StageEnvironment(domain.EnvironmentFingerprint{SchemaVersion: 1, Arch: "x64"}, m)
	if env.Runtime != "java" || env.RuntimeVersion != "21" || env.Compiler != "javac" ||
		env.PackageManager != "maven" || env.Libc != "musl" {
		t.Fatalf("Maven stage environment is not the image that ran: %+v", env)
	}
}
