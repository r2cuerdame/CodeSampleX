package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func gradleManifest(pkgs ...string) domain.SampleManifest {
	return domain.SampleManifest{
		SchemaVersion: 1,
		Packages:      pkgs,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "maven", Runtime: "java", RuntimeVersion: "21",
			ExecutionContext: "java", PackageManager: "gradle",
		},
		BuildCommand:    append([]string(nil), gradleBuildCommand...),
		ContractCommand: append([]string(nil), gradleContractCommand...),
		VerifierAdapter: "gradle-java@1",
	}
}

func TestPrepareGradleResolverUsesOnlyExactManifestCoordinates(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"build.gradle":                       `plugins { id 'com.attacker.plugin' }`,
		"settings.gradle":                    `pluginManagement { repositories { maven { url 'https://attacker.invalid' } } }`,
		"init.gradle":                        `println 'must-not-run'`,
		"gradle.properties":                  `org.gradle.jvmargs=-javaagent:/work/evil.jar`,
		"gradlew":                            `#!/bin/sh\necho must-not-run`,
		"gradle/wrapper/gradle-wrapper.jar":  "attacker wrapper",
		".gradle/init.d/attacker.gradle":     `println 'must-not-run'`,
		".csx-vendor/poison/attacker.gradle": `println 'must-not-survive'`,
		"src/main/java/Example.java":         `class Example {}`,
		"test/Contract.java":                 `class Contract { public static void main(String[] args) {} }`,
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := gradleManifest(
		"pkg:maven/com.fasterxml.jackson.core/jackson-annotations@2.21",
		"pkg:maven/com.fasterxml.jackson.core/jackson-core@2.21.4",
		"pkg:maven/com.fasterxml.jackson.core/jackson-databind@2.21.4",
	)
	if err := prepareGradleResolver(dir, m); err != nil {
		t.Fatal(err)
	}
	resolver := readSandboxFile(t, dir, gradleResolverBuild)
	for _, want := range []string{
		`lockedClasspath "com.fasterxml.jackson.core:jackson-annotations:2.21"`,
		`lockedClasspath "com.fasterxml.jackson.core:jackson-core:2.21.4"`,
		`lockedClasspath "com.fasterxml.jackson.core:jackson-databind:2.21.4"`,
		"transitive = false", "actualCoordinates.toSet() != expectedCoordinates",
	} {
		if !strings.Contains(resolver, want) {
			t.Errorf("generated resolver missing %q:\n%s", want, resolver)
		}
	}
	for _, forbidden := range []string{"attacker", "build.gradle.kts", "gradlew", "apply from", "https://"} {
		if strings.Contains(resolver, forbidden) {
			t.Errorf("generated resolver copied sample-controlled %q: %s", forbidden, resolver)
		}
	}
	settings := readSandboxFile(t, dir, gradleResolverSettings)
	for _, want := range []string{"RepositoriesMode.FAIL_ON_PROJECT_REPOS", "mavenCentral()", "repositories.clear()"} {
		if !strings.Contains(settings, want) {
			t.Errorf("generated settings missing %q: %s", want, settings)
		}
	}
	runner := readSandboxFile(t, dir, gradleRunnerBuild)
	for _, want := range []string{"id 'java'", "'/work/src/main/java'", "'/work/test'", "mainClass = 'Contract'", "options.release = 21", "JavaVersion.VERSION_1_8"} {
		if !strings.Contains(runner, want) {
			t.Errorf("generated offline runner missing %q: %s", want, runner)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".csx-vendor", "poison")); !os.IsNotExist(err) {
		t.Fatalf("author-planted generated output survived: %v", err)
	}
}

func readSandboxFile(t *testing.T, dir, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestGradleManifestIsAClosedExactLockAndFixedContract(t *testing.T) {
	cases := map[string]domain.SampleManifest{
		"range":        gradleManifest("pkg:maven/com.example/lib@%5B1.0,2.0%29"),
		"snapshot":     gradleManifest("pkg:maven/com.example/lib@1.0-SNAPSHOT"),
		"classifier":   gradleManifest("pkg:maven/com.example/lib@1.0.0?classifier=sources"),
		"non jar":      gradleManifest("pkg:maven/com.example/lib@1.0.0?type=pom"),
		"bang version": gradleManifest("pkg:maven/com.example/lib@1.0!1"),
		"dynamic plus": gradleManifest("pkg:maven/com.example/lib@1.2+"),
	}
	badBuild := gradleManifest("pkg:maven/com.example/lib@1.0.0")
	badBuild.BuildCommand = []string{"gradle", "build"}
	cases["sample build"] = badBuild
	badContract := gradleManifest("pkg:maven/com.example/lib@1.0.0")
	badContract.ContractCommand = []string{"./gradlew", "test"}
	cases["sample wrapper"] = badContract
	wrongAdapter := gradleManifest("pkg:maven/com.example/lib@1.0.0")
	wrongAdapter.VerifierAdapter = "maven-java@1"
	cases["wrong adapter"] = wrongAdapter

	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if err := prepareGradleResolver(t.TempDir(), m); err == nil {
				t.Fatalf("unsafe Gradle manifest accepted: %+v", m)
			}
		})
	}
	if err := prepareGradleResolver(t.TempDir(), gradleManifest("pkg:maven/org.apache.commons/commons-lang3@3.17.0")); err != nil {
		t.Fatalf("exact Gradle lock rejected: %v", err)
	}
}

func TestGradleResolveRunsTrustedGeneratedProjectAwayFromSample(t *testing.T) {
	var argv []string
	stubExec(t, func(_ context.Context, _ string, got []string) ([]byte, error) {
		argv = append([]string(nil), got...)
		return []byte("ok"), nil
	})
	res := (DockerRunner{}).Resolve(context.Background(), t.TempDir(),
		gradleManifest("pkg:maven/org.apache.commons/commons-lang3@3.17.0"))
	if res.Result != ResultPass {
		t.Fatalf("resolve = %+v", res)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		gradleJavaImages["21"].image, "--user 0:0", "GRADLE_USER_HOME=/work/.csx-vendor/gradle-home",
		"cp /work/" + gradleResolverBuild + " /tmp/csx-gradle-resolver/build.gradle",
		"cp /work/" + gradleResolverSettings + " /tmp/csx-gradle-resolver/settings.gradle",
		"--project-dir /tmp/csx-gradle-resolver resolveLocked", "gradle-resolved.sha256",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("resolve command missing %q: %s", want, joined)
		}
	}
	for _, forbidden := range []string{
		"/work/build.gradle", "/work/settings.gradle", "/work/init.gradle", "/work/gradlew",
		"/work/gradle/wrapper", "--init-script", "com.attacker.plugin",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("resolve command uses sample-controlled Gradle input %q: %s", forbidden, joined)
		}
	}
	if strings.Contains(joined, "--network=none") {
		t.Errorf("resolve unexpectedly disabled dependency network: %s", joined)
	}
}

func TestGradleBuildAndContractAreFixedGeneratedProjectAndNetworkOff(t *testing.T) {
	var calls [][]string
	stubExec(t, func(_ context.Context, _ string, got []string) ([]byte, error) {
		calls = append(calls, append([]string(nil), got...))
		return []byte("ok"), nil
	})
	m := gradleManifest("pkg:maven/org.apache.commons/commons-lang3@3.17.0")
	// Even a post-resolve in-memory mutation cannot redirect the runner to a
	// sample wrapper or project. The fixed command is selected by adapter.
	m.BuildCommand = []string{"./gradlew", "evil"}
	m.ContractCommand = []string{"gradle", "--init-script", "/work/init.gradle", "evil"}
	r := DockerRunner{}
	if got := r.Build(context.Background(), t.TempDir(), m); got.Result != ResultPass {
		t.Fatal(got.Log)
	}
	if got := r.Contract(context.Background(), t.TempDir(), m); got.Result != ResultPass {
		t.Fatal(got.Log)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want build and contract", len(calls))
	}
	for i, call := range calls {
		joined := strings.Join(call, " ")
		for _, want := range []string{"--network=none", "--offline", "--no-daemon", "--no-scan", "/work/" + gradleRunnerDir} {
			if !strings.Contains(joined, want) {
				t.Errorf("stage %d missing %q: %s", i, want, joined)
			}
		}
		for _, forbidden := range []string{"/work/build.gradle", "/work/settings.gradle", "/work/init.gradle", "/work/gradlew", " evil"} {
			if strings.Contains(joined, forbidden) {
				t.Errorf("stage %d uses sample-controlled input %q: %s", i, forbidden, joined)
			}
		}
	}
}

func TestGradleWorkerRequirementSelectsThePinnedGradleImage(t *testing.T) {
	want := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "gradle-java@1",
		Ecosystem: "maven", Runtime: "java", RuntimeVersion: "21", ExecutionContext: "java",
	}
	if !ContainerSupportsRequirements(want) {
		t.Fatal("worker rejected the pinned Gradle/Java 21 image")
	}
	want.RuntimeVersion = "17"
	if !ContainerSupportsRequirements(want) {
		t.Fatal("worker rejected the pinned Gradle/Java 17 image")
	}
	want.RuntimeVersion = "21"
	want.VerifierAdapter = "gradle-java@2"
	if ContainerSupportsRequirements(want) {
		t.Fatal("worker claimed an unknown Gradle adapter")
	}

	m := gradleManifest("pkg:maven/org.apache.commons/commons-lang3@3.17.0")
	img, err := imageForManifest(m)
	if err != nil || img != gradleJavaImages["21"].image {
		t.Fatalf("Gradle image = %q, %v", img, err)
	}
	env := (DockerRunner{}).StageEnvironment(domain.EnvironmentFingerprint{SchemaVersion: 1, Arch: "x64"}, m)
	if env.Runtime != "java" || env.RuntimeVersion != "21" || env.PackageManager != "gradle" ||
		env.PackageManagerVersion != "8.14.3" || env.Compiler != "javac" || env.CompilerVersion != "21" ||
		env.Distro != "amzn" || env.Libc != "glibc" {
		t.Fatalf("Gradle receipt environment = %+v", env)
	}
}
