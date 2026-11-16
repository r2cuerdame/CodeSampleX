package sandbox

import (
	"context"
	"io"
	"os/exec"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var detectTimeout = 5 * time.Second

var lookDocker = func() error {
	_, err := exec.LookPath("docker")
	return err
}

var dockerProbe = func(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "version")
	prepareCommand(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

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
