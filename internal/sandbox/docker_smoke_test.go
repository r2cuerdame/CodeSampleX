package sandbox

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestDockerSmokeMavenJDKMatrixClassfileMajors(t *testing.T) {
	if os.Getenv("CSX_TEST_DOCKER") != "1" {
		t.Skip("set CSX_TEST_DOCKER=1 to run the real-docker smoke test")
	}
	if Detect(context.Background()) != domain.CapContainerRun {
		t.Skip("docker daemon not available")
	}
	for version, major := range map[string]uint16{"8": 52, "11": 55, "17": 61, "21": 65, "25": 69} {
		t.Run("jdk"+version, func(t *testing.T) {
			dir := t.TempDir()
			specVersion := version
			if version == "8" {
				specVersion = "1.8"
			}
			if err := os.WriteFile(filepath.Join(dir, "Contract.java"), []byte(`public final class Contract {
  public static void main(String[] args) {
    if (!System.getProperty("java.specification.version").equals("`+specVersion+`")) throw new AssertionError("wrong JDK");
  }
}`), 0o644); err != nil {
				t.Fatal(err)
			}
			m := mavenManifest("pkg:maven/org.apache.commons/commons-lang3@3.17.0")
			m.Environment.RuntimeVersion = version
			m.Environment.LanguageVersion = version
			flags := "--release " + version
			if version == "8" {
				flags = "-source 8 -target 8"
			}
			m.BuildCommand = []string{"sh", "-c", "mkdir -p /work/.csx-vendor/classes && javac " + flags + " -d /work/.csx-vendor/classes Contract.java"}
			m.ContractCommand = []string{"java", "-cp", "/work/.csx-vendor/classes", "Contract"}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			r := DockerRunner{}
			if result := r.Build(ctx, dir, m); result.Result != ResultPass {
				t.Fatalf("build: %s\n%s", result.Result, result.Log)
			}
			class, err := os.ReadFile(filepath.Join(dir, ".csx-vendor", "classes", "Contract.class"))
			if err != nil || len(class) < 8 {
				t.Fatalf("read class file: size=%d err=%v", len(class), err)
			}
			if got := binary.BigEndian.Uint16(class[6:8]); got != major {
				t.Fatalf("classfile major = %d, want %d for Java %s", got, major, version)
			}
			if result := r.Contract(ctx, dir, m); result.Result != ResultPass {
				t.Fatalf("contract: %s\n%s", result.Result, result.Log)
			}
		})
	}
}

func TestDockerSmokeGradleJDKMatrixCanaries(t *testing.T) {
	if os.Getenv("CSX_TEST_DOCKER") != "1" {
		t.Skip("set CSX_TEST_DOCKER=1 to run the real-docker smoke test")
	}
	if Detect(context.Background()) != domain.CapContainerRun {
		t.Skip("docker daemon not available")
	}
	for _, version := range []string{"8", "21", "25"} {
		t.Run("jdk"+version, func(t *testing.T) {
			dir := t.TempDir()
			specVersion := version
			if version == "8" {
				specVersion = "1.8"
			}
			write := func(rel, content string) {
				t.Helper()
				path := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("src/main/java/BlankChecks.java", `import org.apache.commons.lang3.StringUtils;
public final class BlankChecks { public static boolean blank(String s) { return StringUtils.isBlank(s); } }`)
			write("test/Contract.java", fmt.Sprintf(`public final class Contract {
  public static void main(String[] args) {
    if (!BlankChecks.blank(" ")) throw new AssertionError("contract");
    if (!System.getProperty("java.specification.version").equals("%s")) throw new AssertionError("wrong JDK");
  }
}`, specVersion))
			m := gradleManifest("pkg:maven/org.apache.commons/commons-lang3@3.17.0")
			m.Environment.RuntimeVersion = version
			m.Environment.LanguageVersion = version
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			r := DockerRunner{}
			if result := r.Resolve(ctx, dir, m); result.Result != ResultPass {
				t.Fatalf("resolve: %s\n%s", result.Result, result.Log)
			}
			if result := r.Build(ctx, dir, m); result.Result != ResultPass {
				t.Fatalf("build: %s\n%s", result.Result, result.Log)
			}
			if result := r.Contract(ctx, dir, m); result.Result != ResultPass {
				t.Fatalf("contract: %s\n%s", result.Result, result.Log)
			}
		})
	}
}

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

// TestDockerSmokePython314 proves that a 3.14 manifest actually executes in
// the digest-pinned Python 3.14 image, rather than merely receiving a 3.14
// label in the receipt environment.
func TestDockerSmokePython314(t *testing.T) {
	if os.Getenv("CSX_TEST_DOCKER") != "1" {
		t.Skip("set CSX_TEST_DOCKER=1 to run the real-docker smoke test")
	}
	if Detect(context.Background()) != domain.CapContainerRun {
		t.Skip("docker daemon not available")
	}

	dir := t.TempDir()
	contract := `import glob, platform, sys
assert sys.version_info[:2] == (3, 14), sys.version
assert glob.glob("/lib/ld-musl-*"), "musl loader not found"
print(platform.python_version())
`
	if err := os.WriteFile(filepath.Join(dir, "contract.py"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	m := domain.SampleManifest{
		SchemaVersion: 1,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "pypi", Runtime: "python",
			RuntimeVersion: "3.14", ExecutionContext: "python",
		},
		ContractCommand: []string{"python", "contract.py"},
		VerifierAdapter: "python@1",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	r := DockerRunner{}
	if res := r.Contract(ctx, dir, m); res.Result != ResultPass {
		t.Fatalf("Python 3.14 contract: %s\n%s", res.Result, res.Log)
	}
	env := r.StageEnvironment(domain.EnvironmentFingerprint{SchemaVersion: 1, Arch: "x64"}, m)
	if env.Runtime != "python" || env.RuntimeVersion != "3.14" || env.Libc != "musl" {
		t.Fatalf("Python 3.14 stage environment = %+v", env)
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

// TestDockerSmokeGradleJava proves the Gradle lane's three separate facts:
// the network-on stage runs only a generated Central resolver from /tmp, the
// network-off build runs real Gradle against the generated Java project, and
// the network-off contract executes the compiled Contract class. Sample
// Gradle files are intentionally hostile canaries and must remain irrelevant.
func TestDockerSmokeGradleJava(t *testing.T) {
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
	write("build.gradle", `throw new GradleException('sample build.gradle must never run')`)
	write("settings.gradle", `throw new GradleException('sample settings.gradle must never run')`)
	write("init.gradle", `throw new GradleException('sample init.gradle must never run')`)
	write("gradle.properties", `org.gradle.jvmargs=-javaagent:/work/does-not-exist.jar`)
	write("gradlew", "#!/bin/sh\nexit 91\n")
	write("src/main/java/BlankChecks.java", `import org.apache.commons.lang3.StringUtils;
public final class BlankChecks {
  public static boolean blank(String value) { return StringUtils.isBlank(value); }
}
`)
	write("test/Contract.java", `public final class Contract {
  public static void main(String[] args) {
    if (!BlankChecks.blank(" \t")) throw new AssertionError("whitespace must be blank");
    if (BlankChecks.blank("x")) throw new AssertionError("text must not be blank");
    System.out.println("gradle contract ok");
  }
}
`)

	m := gradleManifest("pkg:maven/org.apache.commons/commons-lang3@3.17.0")
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
	for _, rel := range []string{gradleResolvedList, gradleResolvedHashes} {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || info.Size() == 0 {
			t.Fatalf("Gradle resolve evidence %s missing or empty: %v", rel, err)
		}
	}
	env := r.StageEnvironment(domain.EnvironmentFingerprint{SchemaVersion: 1, Arch: "x64"}, m)
	if env.Runtime != "java" || env.RuntimeVersion != "21" || env.Compiler != "javac" ||
		env.PackageManager != "gradle" || env.PackageManagerVersion != "8.14" || env.Libc != "musl" {
		t.Fatalf("Gradle stage environment is not the image that ran: %+v", env)
	}
}
