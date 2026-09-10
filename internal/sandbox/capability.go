package sandbox

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// detectTimeout bounds the docker daemon probe.
var detectTimeout = 5 * time.Second

// lookDocker and dockerProbe are package variables so tests can simulate
// docker's presence/absence without a docker install.
var lookDocker = func() error {
	_, err := exec.LookPath("docker")
	return err
}

var dockerProbe = func(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "version")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// Detect reports the strongest sandbox capability this host offers:
// CONTAINER_RUN when the docker CLI exists and the daemon answers
// `docker version` within 5s, COMPILE_ONLY otherwise. The result feeds
// the verification receipt verbatim — never claim more than detected.
func Detect(ctx context.Context) domain.SandboxCapability {
	if lookDocker() != nil {
		return domain.CapCompileOnly
	}
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	if dockerProbe(ctx) != nil {
		return domain.CapCompileOnly
	}
	return domain.CapContainerRun
}

// supportsLinuxContainers reports whether this host has a responsive Docker daemon
// capable of running Linux containers. It checks that docker exists in PATH,
// the daemon responds within detectTimeout, and the daemon runs Linux containers.
// On Windows hosts, Linux container execution is skipped unless explicitly enabled
// via CSX_TEST_DOCKER=1 with a verified Linux-container daemon.
func supportsLinuxContainers(ctx context.Context) (bool, string) {
	return supportsLinuxContainersOn(runtime.GOOS, os.Getenv, ctx)
}

func supportsLinuxContainersOn(goos string, getenv func(string) string, ctx context.Context) (bool, string) {
	if goos == "windows" && getenv("CSX_TEST_DOCKER") != "1" {
		return false, "Linux container capability is unavailable on Windows"
	}
	if lookDocker() != nil {
		return false, "docker not available"
	}
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	if Detect(ctx) != domain.CapContainerRun {
		return false, "docker daemon not available"
	}
	if DetectContainerOS(ctx) != ContainerOSLinux {
		return false, "docker daemon does not support Linux containers"
	}
	return true, ""
}
