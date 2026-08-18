package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

const (
	gradleResolverDir      = ".csx-vendor/gradle-resolver"
	gradleResolverBuild    = gradleResolverDir + "/build.gradle"
	gradleResolverSettings = gradleResolverDir + "/settings.gradle"
	gradleRunnerDir        = ".csx-vendor/gradle-runner"
	gradleRunnerBuild      = gradleRunnerDir + "/build.gradle"
	gradleRunnerSettings   = gradleRunnerDir + "/settings.gradle"
	gradleResolvedList     = ".csx-vendor/gradle-resolved.tsv"
	gradleResolvedHashes   = ".csx-vendor/gradle-resolved.sha256"
)

var (
	gradleBuildCommand = []string{
		"gradle", "--offline", "--no-daemon", "--no-scan", "--console=plain",
		"--project-dir", "/work/" + gradleRunnerDir, "classes",
	}
	gradleContractCommand = []string{
		"gradle", "--offline", "--no-daemon", "--no-scan", "--console=plain",
		"--project-dir", "/work/" + gradleRunnerDir, "contract",
	}
)

// prepareGradleResolver creates both Gradle projects the verifier is allowed
// to execute. Nothing from the sample's build.gradle, settings.gradle,
// init.gradle, wrapper, gradle.properties or plugin declarations is copied.
//
// The network-enabled project does one thing: fetch the exact canonical Maven
// purls in the manifest from Maven Central with transitive expansion disabled.
// The network-off project does one different thing: compile src/main/java and
// test/Contract.java against only those copied JARs, then run Contract through
// a built-in JavaExec task. Both use only Gradle core plugins bundled in the
// digest-pinned image.
func prepareGradleResolver(dir string, m domain.SampleManifest) error {
	if m.VerifierAdapter != "gradle-java@1" {
		return fmt.Errorf("sandbox: Gradle resolver requires verifier adapter gradle-java@1")
	}
	if !sameCommand(m.BuildCommand, gradleBuildCommand) {
		return fmt.Errorf("sandbox: gradle-java@1 requires the fixed offline Gradle build command")
	}
	if !sameCommand(m.ContractCommand, gradleContractCommand) {
		return fmt.Errorf("sandbox: gradle-java@1 requires the fixed offline Gradle contract command")
	}
	coords, err := lockedMavenCoordinates(m)
	if err != nil {
		return err
	}
	spec, java, err := javaImageForManifest(m)
	if err != nil || !java {
		if err != nil {
			return err
		}
		return fmt.Errorf("sandbox: Gradle resolver requires a supported Java runtime")
	}
	for _, c := range coords {
		// lockedMavenCoordinates already rejects ranges and SNAPSHOTs. This
		// second, Gradle-specific constraint makes every generated Groovy
		// string and every copied-JAR path inert data.
		if !validGradleCoordinatePart(c.Version) {
			return fmt.Errorf("sandbox: Maven package version %q is unsafe for the Gradle resolver", c.Version)
		}
	}

	vendorRoot := filepath.Join(dir, ".csx-vendor")
	if err := os.RemoveAll(vendorRoot); err != nil {
		return fmt.Errorf("sandbox: clear generated Gradle resolver: %w", err)
	}
	for _, rel := range []string{gradleResolverDir, gradleRunnerDir} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o755); err != nil {
			return fmt.Errorf("sandbox: create generated Gradle project: %w", err)
		}
	}

	resolverBuild := renderGradleResolverBuild(coords)
	files := map[string]string{
		gradleResolverBuild:    resolverBuild,
		gradleResolverSettings: gradleResolverSettingsText,
		gradleRunnerBuild:      renderGradleRunnerBuild(gradleLanguageRelease(m, spec)),
		gradleRunnerSettings:   gradleRunnerSettingsText,
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			return fmt.Errorf("sandbox: write generated Gradle project: %w", err)
		}
	}
	return nil
}

func gradleLanguageRelease(m domain.SampleManifest, spec javaVerifierImage) string {
	if m.Environment.LanguageVersion != "" {
		return m.Environment.LanguageVersion
	}
	return spec.runtimeVersion
}

func sameCommand(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func validGradleCoordinatePart(part string) bool {
	if part == "" {
		return false
	}
	for _, r := range part {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func renderGradleResolverBuild(coords []mavenCoordinate) string {
	coords = append([]mavenCoordinate(nil), coords...)
	sort.Slice(coords, func(i, j int) bool {
		if coords[i].GroupID != coords[j].GroupID {
			return coords[i].GroupID < coords[j].GroupID
		}
		return coords[i].ArtifactID < coords[j].ArtifactID
	})

	var dependencies, expected strings.Builder
	for _, c := range coords {
		gav := c.GroupID + ":" + c.ArtifactID + ":" + c.Version
		fmt.Fprintf(&dependencies, "    lockedClasspath %q\n", gav)
		fmt.Fprintf(&expected, "    %q,\n", gav)
	}
	return fmt.Sprintf(`plugins {
    id 'base'
}

configurations {
    lockedClasspath {
        canBeConsumed = false
        canBeResolved = true
        transitive = false
    }
}

dependencies {
%s}

def expectedCoordinates = [
%s] as Set

tasks.register('resolveLocked') {
    doLast {
        def artifacts = configurations.lockedClasspath.resolvedConfiguration.resolvedArtifacts
        def actualCoordinates = artifacts.collect {
            "${it.moduleVersion.id.group}:${it.name}:${it.moduleVersion.id.version}"
        }
        if (actualCoordinates.size() != expectedCoordinates.size() || actualCoordinates.toSet() != expectedCoordinates) {
            throw new GradleException("resolved coordinates differ from the exact manifest lock: ${actualCoordinates}")
        }

        def jarRoot = file('/work/.csx-vendor/gradle-jars')
        def receipt = file('/work/.csx-vendor/gradle-resolved.tsv')
        delete jarRoot
        jarRoot.mkdirs()
        receipt.setText('', 'UTF-8')

        artifacts.sort { a, b ->
            def left = "${a.moduleVersion.id.group}:${a.name}:${a.moduleVersion.id.version}"
            def right = "${b.moduleVersion.id.group}:${b.name}:${b.moduleVersion.id.version}"
            left <=> right
        }.each { artifact ->
            def id = artifact.moduleVersion.id
            def relative = "${id.group.replace('.', '/')}/${artifact.name}/${id.version}/${artifact.file.name}"
            def target = new File(jarRoot, relative)
            target.parentFile.mkdirs()
            project.copy {
                from artifact.file
                into target.parentFile
            }
            receipt.append("pkg:maven/${id.group}/${artifact.name}@${id.version}\tgradle-jars/${relative}\n", 'UTF-8')
        }
    }
}
`, dependencies.String(), expected.String())
}

const gradleResolverSettingsText = `import org.gradle.api.initialization.resolve.RepositoriesMode

pluginManagement {
    repositories.clear()
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        mavenCentral()
    }
}

rootProject.name = 'csx-locked-resolver'
`

const gradleRunnerSettingsText = `pluginManagement {
    repositories.clear()
}

rootProject.name = 'csx-offline-contract'
`

func renderGradleRunnerBuild(release string) string {
	return fmt.Sprintf(`plugins {
    id 'java'
}

def lockedJars = fileTree(dir: '/work/.csx-vendor/gradle-jars', include: '**/*.jar')

sourceSets {
    main {
        java.setSrcDirs(['/work/src/main/java'])
        compileClasspath += lockedJars
        runtimeClasspath += lockedJars
    }
    contract {
        java.setSrcDirs(['/work/test'])
        compileClasspath += sourceSets.main.output + lockedJars
        runtimeClasspath += output + sourceSets.main.output + lockedJars
    }
}

tasks.withType(JavaCompile).configureEach {
    if (JavaVersion.current() == JavaVersion.VERSION_1_8) {
        sourceCompatibility = '1.8'
        targetCompatibility = '1.8'
    } else {
        options.release = %s
    }
}

tasks.register('contract', JavaExec) {
    dependsOn tasks.named('contractClasses')
    classpath = sourceSets.contract.runtimeClasspath
    mainClass = 'Contract'
}
`, release)
}
