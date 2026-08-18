package sandbox

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestJavaRuntimeVersionSelectsPinnedMatrixImagesAndHonestReceipts(t *testing.T) {
	host := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "windows", Arch: "arm64"}
	for adapter, images := range map[string]map[string]javaVerifierImage{
		"maven-java@1":  mavenJavaImages,
		"gradle-java@1": gradleJavaImages,
	} {
		for _, version := range []string{"8", "11", "17", "21", "25"} {
			t.Run(adapter+"-"+version, func(t *testing.T) {
				m := domain.SampleManifest{VerifierAdapter: adapter, Environment: domain.EnvironmentFingerprint{
					SchemaVersion: 1, Ecosystem: "maven", Runtime: "java", RuntimeVersion: version,
					ExecutionContext: "java",
				}}
				img, err := imageForManifest(m)
				if err != nil || img != images[version].image {
					t.Fatalf("image = %q, %v; want %q", img, err, images[version].image)
				}
				env := (DockerRunner{}).StageEnvironment(host, m)
				if env.Runtime != "java" || env.RuntimeVersion != version || env.Compiler != "javac" ||
					env.CompilerVersion != version || env.PackageManagerVersion != images[version].packageManagerVersion ||
					env.OSVersionBucket != "2023" || env.Distro != "amzn" || env.Libc != "glibc" || env.Arch != "arm64" {
					t.Fatalf("receipt environment = %+v", env)
				}
				if !ContainerSupportsRequirements(domain.WorkerRequirements{
					SandboxCapability: domain.CapContainerRun, VerifierAdapter: adapter,
					Ecosystem: "maven", Runtime: "java", RuntimeVersion: version, ExecutionContext: "java",
				}) {
					t.Fatal("worker rejected a pinned Java matrix image")
				}
			})
		}
	}
}

func TestOmittedMavenJavaVersionPreservesLegacyJava21AlpineLane(t *testing.T) {
	m := mavenManifest("pkg:maven/org.example/library@1.2.3")
	img, err := imageForManifest(m)
	if err != nil || img != mavenJavaImage {
		t.Fatalf("default Maven image = %q, %v; want %q", img, err, mavenJavaImage)
	}
	env := (DockerRunner{}).StageEnvironment(domain.EnvironmentFingerprint{SchemaVersion: 1, Arch: "x64"}, m)
	if env.RuntimeVersion != "21" || env.PackageManagerVersion != "3.9" || env.Distro != "" || env.Libc != "musl" {
		t.Fatalf("legacy Maven receipt changed = %+v", env)
	}
}

func TestJavaRuntimeVersionIsExactAndFailClosed(t *testing.T) {
	for _, adapter := range []string{"maven-java@1", "gradle-java@1"} {
		for _, version := range []string{"7", "9", "21.0.8", "22", "26"} {
			m := domain.SampleManifest{VerifierAdapter: adapter, Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "maven", Runtime: "java", RuntimeVersion: version,
			}}
			if _, err := imageForManifest(m); err == nil || !strings.Contains(err.Error(), version) {
				t.Errorf("%s Java %s was not rejected clearly: %v", adapter, version, err)
			}
		}
	}
}

func TestGradleRunnerReleaseTracksSelectedJDK(t *testing.T) {
	for _, version := range []string{"8", "11", "17", "21", "25"} {
		m := gradleManifest("pkg:maven/org.example/library@1.2.3")
		m.Environment.RuntimeVersion = version
		dir := t.TempDir()
		if err := prepareGradleResolver(dir, m); err != nil {
			t.Fatal(err)
		}
		body := readSandboxFile(t, dir, gradleRunnerBuild)
		if !strings.Contains(body, "options.release = "+version) {
			t.Fatalf("Java %s runner did not select its release:\n%s", version, body)
		}
	}
}

func TestGradleMatrixKeepsAnExplicitLanguageTarget(t *testing.T) {
	for _, version := range []string{"8", "11", "17", "21", "25"} {
		m := gradleManifest("pkg:maven/org.example/library@1.2.3")
		m.Environment.RuntimeVersion = version
		m.Environment.LanguageVersion = "8"
		dir := t.TempDir()
		if err := prepareGradleResolver(dir, m); err != nil {
			t.Fatal(err)
		}
		body := readSandboxFile(t, dir, gradleRunnerBuild)
		if !strings.Contains(body, "options.release = 8") {
			t.Fatalf("Java %s runner lost language target 8:\n%s", version, body)
		}
		env := (DockerRunner{}).StageEnvironment(domain.EnvironmentFingerprint{SchemaVersion: 1, Arch: "x64"}, m)
		if env.LanguageVersion != "8" || env.RuntimeVersion != version {
			t.Fatalf("Java %s receipt = %+v", version, env)
		}
	}
}

func TestJavaLanguageTargetCannotExceedRuntime(t *testing.T) {
	m := gradleManifest("pkg:maven/org.example/library@1.2.3")
	m.Environment.RuntimeVersion = "8"
	m.Environment.LanguageVersion = "11"
	if _, err := imageForManifest(m); err == nil {
		t.Fatal("Java 11 language target was accepted on JDK 8")
	}
	m.Environment.RuntimeVersion = "21"
	m.Environment.LanguageVersion = "8; throw new Error()"
	if _, err := imageForManifest(m); err == nil {
		t.Fatal("non-numeric/injected Java language target was accepted")
	}
	m.Environment.RuntimeVersion = "25"
	m.Environment.LanguageVersion = "21"
	if _, err := imageForManifest(m); err != nil {
		t.Fatalf("Java 21 target on JDK 25 was rejected: %v", err)
	}
	m.Environment.LanguageVersion = ""
	env := (DockerRunner{}).StageEnvironment(domain.EnvironmentFingerprint{SchemaVersion: 1, Arch: "amd64"}, m)
	if env.LanguageVersion != "25" {
		t.Fatalf("omitted language target = %q, want runtime line 25", env.LanguageVersion)
	}
}
