package sandbox

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func windowsManifest(ecosystem, runtime, version string) domain.SampleManifest {
	return domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: ecosystem, Runtime: runtime, RuntimeVersion: version,
	}}
}

// A Windows daemon runs Windows images. Selecting a Linux image there
// would either fail to start or, worse, run and be filed as Windows
// evidence for a run that never happened on Windows.
func TestWindowsDaemonSelectsWindowsImages(t *testing.T) {
	for _, c := range []struct{ eco, runtime, version, want string }{
		{"golang", "", "", "golang:1.26-windowsservercore-ltsc2022"},
		{"golang", "go", "", "golang:1.26-windowsservercore-ltsc2022"},
		{"pypi", "python", "", "python:3.12-windowsservercore-ltsc2022"},
		{"pypi", "python", "3.14", "python:3.14-windowsservercore-ltsc2022"},
	} {
		got, err := imageForManifestOn(ContainerOSWindows, windowsManifest(c.eco, c.runtime, c.version))
		if err != nil || got != c.want {
			t.Errorf("windows image for %s/%s/%s = %q err=%v, want %q", c.eco, c.runtime, c.version, got, err, c.want)
		}
	}
}

// Node publishes no official Windows image, and Java and browser work has
// none either. A worker must skip that work and keep scanning rather than
// verify it against an image this project would have to build itself.
func TestWindowsDaemonRefusesEcosystemsWithNoWindowsImage(t *testing.T) {
	for _, c := range []struct{ eco, runtime string }{
		{"npm", "node"}, {"npm", "bun"}, {"cargo", ""}, {"gem", ""}, {"maven", "java"},
	} {
		if img, err := imageForManifestOn(ContainerOSWindows, windowsManifest(c.eco, c.runtime, "")); err == nil {
			t.Errorf("windows daemon offered %q for %s/%s, want a refusal", img, c.eco, c.runtime)
		}
	}
	// The pre-claim decision must agree, or the worker claims work it
	// cannot run and strands the job.
	npm := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, Ecosystem: "npm",
		Runtime: "node", VerifierAdapter: "node-typescript@1",
	}
	if ContainerSupportsRequirementsOn(ContainerOSWindows, npm) {
		t.Error("a Windows worker would claim npm work it has no image for")
	}
	if !ContainerSupportsRequirementsOn(ContainerOSLinux, npm) {
		t.Error("a Linux worker stopped accepting npm work")
	}
	// A requirement-free legacy job could be any ecosystem; a Windows
	// daemon has images for two and must not gamble.
	if ContainerSupportsRequirementsOn(ContainerOSWindows, domain.WorkerRequirements{}) {
		t.Error("a Windows worker claimed a requirement-free job")
	}
}

// A job that names an OS is work for a daemon serving that OS. The queue
// already filters on it; the pre-claim decision must agree, or a worker
// claims work it cannot run and burns the sample's bounded cross attempts.
func TestOSRequirementGatesTheClaim(t *testing.T) {
	windowsWork := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, Ecosystem: "golang", OS: "windows",
	}
	if ContainerSupportsRequirementsOn(ContainerOSLinux, windowsWork) {
		t.Error("a Linux daemon would claim windows-pinned work")
	}
	if !ContainerSupportsRequirementsOn(ContainerOSWindows, windowsWork) {
		t.Error("a Windows daemon was refused windows-pinned golang work")
	}
	linuxWork := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, Ecosystem: "golang", OS: "linux",
	}
	if !ContainerSupportsRequirementsOn(ContainerOSLinux, linuxWork) {
		t.Error("a Linux daemon was refused linux-pinned work")
	}
	// An empty container OS has always meant linux.
	if !ContainerSupportsRequirementsOn("", linuxWork) {
		t.Error("the default daemon stopped being linux")
	}
	if ContainerSupportsRequirementsOn(ContainerOSWindows, linuxWork) {
		t.Error("a Windows daemon would claim linux-pinned work")
	}
}

// The receipt has to describe the container it actually ran in.
func TestWindowsStageEnvironmentReportsWindows(t *testing.T) {
	m := windowsManifest("golang", "go", "")
	env := DockerRunner{ContainerOS: ContainerOSWindows}.
		StageEnvironment(domain.EnvironmentFingerprint{Arch: "x64"}, m)
	if env.OS != "windows" {
		t.Errorf("OS = %q, want windows", env.OS)
	}
	if env.Virtualization != "container" {
		t.Errorf("virtualization = %q, want container", env.Virtualization)
	}
	if env.OSVersionBucket != "windowsservercore" {
		t.Errorf("osVersionBucket = %q, want windowsservercore", env.OSVersionBucket)
	}
	if env.Libc != "" {
		t.Errorf("libc = %q, want empty — Windows has none", env.Libc)
	}
	// The default is unchanged for every existing worker.
	linux := DockerRunner{}.StageEnvironment(domain.EnvironmentFingerprint{Arch: "x64"}, m)
	if linux.OS != "linux" || linux.Libc == "" {
		t.Errorf("linux stage env = %+v, want an unchanged linux/libc receipt", linux)
	}
}

// A Windows container mounts at C:\work and takes no --pids-limit: the
// Windows isolation layer implements none, and passing it makes docker
// refuse the run outright.
func TestWindowsDockerArgs(t *testing.T) {
	args := dockerArgsOn(ContainerOSWindows, "golang:1.26-windowsservercore-ltsc2022",
		`C:\tmp\ws`, true, nil, []string{"go", "build"}, "job")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `C:\tmp\ws:C:\work`) || !strings.Contains(joined, `-w C:\work`) {
		t.Errorf("windows args do not mount C:\\work: %v", args)
	}
	if strings.Contains(joined, "--pids-limit") {
		t.Errorf("windows args pass --pids-limit, which docker refuses: %v", args)
	}
	if !strings.Contains(joined, "--memory=512m") || !strings.Contains(joined, "--network=none") {
		t.Errorf("windows args dropped a resource or network limit: %v", args)
	}
	linux := strings.Join(dockerArgsOn(ContainerOSLinux, "golang:1.26-alpine",
		"/tmp/ws", true, nil, []string{"go", "build"}, "job"), " ")
	if !strings.Contains(linux, "--pids-limit=256") || !strings.Contains(linux, "/tmp/ws:/work") {
		t.Errorf("linux args changed: %s", linux)
	}
}
