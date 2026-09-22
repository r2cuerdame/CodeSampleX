package sandbox

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// linuxContainersAvailable is the gate for tests that start Linux images.
//
// exec.LookPath("docker") was the whole gate until 2026-09, and on a hosted
// Windows runner it is true: docker.exe is installed, the daemon serves
// Windows containers, and every `docker run` of a Linux image stalls for
// 15-20s before failing. Twenty-five images later internal/sandbox had
// spent 455s of a 600s package deadline on nothing, and on the next runner
// it hit the deadline -- which cancelled release v0.1.156 and fail-closed
// the production deploy behind it (#285).
//
// So a Windows host is out unless CSX_TEST_DOCKER=1 says the developer has
// a Linux-mode daemon and wants the images run, and the daemon is then
// asked to *say* linux: DetectContainerOS answers Linux for a probe that
// failed, and a failed probe is exactly the case this gate exists to keep
// out. Each probe gets its own detectTimeout.
func linuxContainersAvailable(ctx context.Context) (bool, string) {
	return linuxContainersAvailableOn(ctx, runtime.GOOS, os.Getenv)
}

func linuxContainersAvailableOn(ctx context.Context, goos string, getenv func(string) string) (bool, string) {
	if goos == "windows" && getenv("CSX_TEST_DOCKER") != "1" {
		return false, "Linux container images are not run on a Windows host unless CSX_TEST_DOCKER=1 (#285)"
	}
	if lookDocker() != nil {
		return false, "docker not available"
	}
	if Detect(ctx) != domain.CapContainerRun {
		return false, "docker daemon not available"
	}
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	out, err := dockerServerOSProbe(ctx)
	if err != nil {
		return false, "docker daemon did not report which containers it serves: " + err.Error()
	}
	if serves := strings.ToLower(strings.TrimSpace(string(out))); serves != ContainerOSLinux {
		return false, "docker daemon serves " + serves + " containers, not linux"
	}
	return true, ""
}

func TestLinuxContainerGateAdmitsOnlyAnAffirmativeLinuxDaemon(t *testing.T) {
	oldLook, oldProbe, oldServerOS := lookDocker, dockerProbe, dockerServerOSProbe
	t.Cleanup(func() { lookDocker, dockerProbe, dockerServerOSProbe = oldLook, oldProbe, oldServerOS })
	optIn := func(k string) string {
		if k == "CSX_TEST_DOCKER" {
			return "1"
		}
		return ""
	}
	noEnv := func(string) string { return "" }

	// A Windows host without the opt-in is refused before any probe runs;
	// the probes are what cost 455s on the hosted runner.
	probed := false
	lookDocker = func() error { probed = true; return nil }
	dockerProbe = func(context.Context) error { probed = true; return nil }
	dockerServerOSProbe = func(context.Context) ([]byte, error) { probed = true; return []byte("linux"), nil }
	if ok, reason := linuxContainersAvailableOn(context.Background(), "windows", noEnv); ok || probed || !strings.Contains(reason, "CSX_TEST_DOCKER") {
		t.Fatalf("windows without opt-in: ok=%v probed=%v reason=%q", ok, probed, reason)
	}

	for _, tc := range []struct {
		name     string
		goos     string
		getenv   func(string) string
		look     error
		daemon   error
		serves   string
		probeErr error
		want     bool
		reason   string
	}{
		{"windows opted in, no docker", "windows", optIn, errors.New("no docker"), nil, "", nil, false, "docker not available"},
		{"windows opted in, daemon down", "windows", optIn, nil, errors.New("down"), "", nil, false, "daemon not available"},
		{"windows opted in, windows daemon", "windows", optIn, nil, nil, "windows\n", nil, false, "serves windows"},
		{"windows opted in, probe failed", "windows", optIn, nil, nil, "", errors.New("timeout"), false, "did not report"},
		{"windows opted in, linux daemon", "windows", optIn, nil, nil, "linux\n", nil, true, ""},
		{"linux, no docker", "linux", noEnv, errors.New("no docker"), nil, "", nil, false, "docker not available"},
		{"linux, daemon down", "linux", noEnv, nil, errors.New("down"), "", nil, false, "daemon not available"},
		{"linux, windows daemon", "linux", noEnv, nil, nil, "Windows", nil, false, "serves windows"},
		{"linux, probe failed", "linux", noEnv, nil, nil, "", errors.New("timeout"), false, "did not report"},
		{"linux, linux daemon", "linux", noEnv, nil, nil, "linux", nil, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookDocker = func() error { return tc.look }
			dockerProbe = func(context.Context) error { return tc.daemon }
			dockerServerOSProbe = func(context.Context) ([]byte, error) { return []byte(tc.serves), tc.probeErr }
			ok, reason := linuxContainersAvailableOn(context.Background(), tc.goos, tc.getenv)
			if ok != tc.want || !strings.Contains(reason, tc.reason) {
				t.Fatalf("got ok=%v reason=%q, want ok=%v reason containing %q", ok, reason, tc.want, tc.reason)
			}
		})
	}
}

// A daemon that accepts the connection and never answers must not hold
// DetectContainerOS: the worker calls it with context.Background().
func TestDetectContainerOSIsBoundedByDetectTimeout(t *testing.T) {
	oldServerOS, oldTimeout := dockerServerOSProbe, detectTimeout
	detectTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dockerServerOSProbe, detectTimeout = oldServerOS, oldTimeout })
	dockerServerOSProbe = func(ctx context.Context) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	got := DetectContainerOS(context.Background())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("DetectContainerOS waited %v on a silent daemon; detectTimeout is %v", elapsed, detectTimeout)
	}
	if got != ContainerOSLinux {
		t.Fatalf("silent daemon reported as %q, want the documented Linux fallback", got)
	}
}
