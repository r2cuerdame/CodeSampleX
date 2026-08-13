package sandbox

import (
	"context"
	"io"
	"os/exec"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// detectTimeout bounds the docker daemon probe.
const detectTimeout = 5 * time.Second

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
