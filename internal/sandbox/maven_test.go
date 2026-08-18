package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func mavenManifest(pkgs ...string) domain.SampleManifest {
	return domain.SampleManifest{
		SchemaVersion: 1,
		Packages:      pkgs,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "maven", Runtime: "java", PackageManager: "maven",
		},
		BuildCommand:    []string{"javac", "Contract.java"},
		ContractCommand: []string{"java", "Contract"},
		VerifierAdapter: "maven-java@1",
	}
}

func TestPrepareMavenResolverUsesOnlyExactManifestCoordinates(t *testing.T) {
	dir := t.TempDir()
	// All of these are attacker-controlled and must be irrelevant to resolve.
	for name, body := range map[string]string{
		"pom.xml":                 `<build><plugins><plugin><artifactId>evil</artifactId></plugin></plugins></build>`,
		".mvn/extensions.xml":     `<extension><artifactId>evil-extension</artifactId></extension>`,
		".mvn/maven.config":       `-s /work/attacker-settings.xml`,
		"attacker-settings.xml":   `<settings><mirrors><mirror><url>https://attacker.invalid</url></mirror></mirrors></settings>`,
		".csx-vendor/poison/file": "old generated output",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := mavenManifest(
		"pkg:maven/com.fasterxml.jackson.core/jackson-databind@2.21.1",
		"pkg:maven/com.fasterxml.jackson.core/jackson-annotations@2.21",
	)
	if err := prepareMavenResolver(dir, m); err != nil {
		t.Fatal(err)
	}
	pom, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(mavenResolverPOM)))
	if err != nil {
		t.Fatal(err)
	}
	text := string(pom)
	for _, want := range []string{
		"<groupId>com.fasterxml.jackson.core</groupId>",
		"<artifactId>jackson-annotations</artifactId>",
		"<artifactId>jackson-databind</artifactId>",
		"<checksumPolicy>fail</checksumPolicy>",
		"<enabled>false</enabled>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated resolver POM missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"evil", "attacker.invalid", "<build>", "<plugin>"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("generated resolver copied sample-controlled %q: %s", forbidden, text)
		}
	}
	settings, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(mavenResolverConfig)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "<mirrorOf>*</mirrorOf>") ||
		!strings.Contains(string(settings), "https://repo.maven.apache.org/maven2") {
		t.Fatalf("settings do not force Maven Central: %s", settings)
	}
	if _, err := os.Stat(filepath.Join(dir, ".csx-vendor", "poison")); !os.IsNotExist(err) {
		t.Fatalf("author-planted generated output survived: %v", err)
	}
}

func TestMavenManifestIsAClosedExactLock(t *testing.T) {
	for name, pkgs := range map[string][]string{
		"no packages":        nil,
		"range":              {"pkg:maven/com.example/lib@%5B1.0,2.0%29"},
		"snapshot":           {"pkg:maven/com.example/lib@1.0-SNAPSHOT"},
		"wrong ecosystem":    {"pkg:npm/react@19.0.0"},
		"noncanonical":       {"pkg:MAVEN/com.example/lib@1.0.0"},
		"missing artifact":   {"pkg:maven/com.example@1.0.0"},
		"path-like artifact": {"pkg:maven/com.example/a/b@1.0.0"},
		"two versions": {
			"pkg:maven/com.example/lib@1.0.0",
			"pkg:maven/com.example/lib@2.0.0",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := lockedMavenCoordinates(mavenManifest(pkgs...)); err == nil {
				t.Fatalf("unsafe Maven package set accepted: %v", pkgs)
			}
		})
	}
	if _, err := lockedMavenCoordinates(mavenManifest(
		"pkg:maven/org.example/lib@1.0.0",
		"pkg:maven/org.example/lib-api@1.0.0",
	)); err != nil {
		t.Fatalf("exact closed lock rejected: %v", err)
	}
}

func TestMavenResolveRunsTrustedGeneratedProjectAwayFromSample(t *testing.T) {
	var argv []string
	stubExec(t, func(_ context.Context, _ string, got []string) ([]byte, error) {
		argv = append([]string(nil), got...)
		return []byte("ok"), nil
	})
	dir := t.TempDir()
	res := (DockerRunner{}).Resolve(context.Background(), dir,
		mavenManifest("pkg:maven/org.example/library@1.2.3"))
	if res.Result != ResultPass {
		t.Fatalf("resolve = %+v", res)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		mavenJavaImage,
		"cp /work/" + mavenResolverPOM + " /tmp/csx-maven-resolver/pom.xml",
		mavenDependencyPlugin + ":copy-dependencies",
		mavenDependencyPlugin + ":list",
		"-DexcludeTransitive=true",
		"--strict-checksums",
		"cd /tmp/csx-maven-resolver",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("resolve command missing %q: %s", want, joined)
		}
	}
	for _, forbidden := range []string{"-f /work/pom.xml", "mvn test", "dependency:go-offline", "/work/.mvn"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("resolve command uses sample-controlled Maven input %q: %s", forbidden, joined)
		}
	}
}

func TestMavenResolveRejectsManifestBeforeStartingContainer(t *testing.T) {
	called := false
	stubExec(t, func(_ context.Context, _ string, _ []string) ([]byte, error) {
		called = true
		return nil, nil
	})
	res := (DockerRunner{}).Resolve(context.Background(), t.TempDir(),
		mavenManifest("pkg:maven/org.example/library@%5B1,2%29"))
	if res.Result != ResultFail || called {
		t.Fatalf("unsafe manifest resolve = %+v, container called=%v", res, called)
	}
}

func TestMavenManifestUsesJavaExecutionContext(t *testing.T) {
	m := mavenManifest("pkg:maven/org.example/library@1.2.3")
	m.Environment.ExecutionContext = "java"
	image, err := imageForManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if image != mavenJavaImage {
		t.Fatalf("image = %q, want %q", image, mavenJavaImage)
	}

	env := (DockerRunner{}).StageEnvironment(
		domain.EnvironmentFingerprint{SchemaVersion: 1, Arch: "x64"}, m)
	if env.ExecutionContext != "java" || env.Runtime != "java" {
		t.Fatalf("Maven environment = runtime %q, context %q", env.Runtime, env.ExecutionContext)
	}
}
