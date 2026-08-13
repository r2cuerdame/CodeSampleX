package sandbox

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// DockerRunner implements the CONTAINER_RUN pipeline (plan C13): the
// resolve stage runs in a network-enabled container, build and contract
// reuse the same bind-mounted workdir with --network=none. The mounted
// dir is always an unpacked sample in a temp workspace — never the host
// project. No host environment is passed into any container.
type DockerRunner struct{}

// imageFor maps an ecosystem to its pinned verifier image.
func imageFor(ecosystem string) (string, error) {
	switch ecosystem {
	case "npm":
		return "node:22-alpine", nil
	case "pypi":
		return "python:3.12-alpine", nil
	case "golang":
		return "golang:1.26-alpine", nil
	case "cargo":
		return "rust:1-alpine", nil
	}
	return "", fmt.Errorf("sandbox: no verifier image for ecosystem %q", ecosystem)
}

// dockerArgs builds the docker run invocation. Resource limits per plan
// C13: --memory=512m --pids-limit=256. No --env flags: the container
// keeps only its image-default PATH/HOME.
func dockerArgs(image, dir string, networkOff bool, cmd []string) []string {
	args := []string{"docker", "run", "--rm"}
	if networkOff {
		args = append(args, "--network=none")
	}
	args = append(args,
		"--memory=512m", "--pids-limit=256",
		"-v", dir+":/work", "-w", "/work",
		image,
	)
	return append(args, cmd...)
}

func (DockerRunner) stage(ctx context.Context, dir string, m domain.SampleManifest, networkOff bool, cmd []string) StageResult {
	img, err := imageFor(m.Environment.Ecosystem)
	if err != nil {
		return StageResult{Result: ResultFail, Log: err.Error()}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return StageResult{Result: ResultFail, Log: "sandbox: resolve workdir: " + err.Error()}
	}
	return runStage(ctx, "", dockerArgs(img, abs, networkOff, cmd))
}

// Resolve fetches dependencies with the network ON but lifecycle scripts OFF.
func (r DockerRunner) Resolve(ctx context.Context, dir string, m domain.SampleManifest) StageResult {
	cmd, err := resolveCommand(m.Environment.Ecosystem)
	if err != nil {
		return StageResult{Result: ResultFail, Log: err.Error()}
	}
	return r.stage(ctx, dir, m, false, cmd)
}

// Build runs the manifest's build command with the network OFF.
func (r DockerRunner) Build(ctx context.Context, dir string, m domain.SampleManifest) StageResult {
	if len(m.BuildCommand) == 0 {
		return skipped("no build command in manifest")
	}
	return r.stage(ctx, dir, m, true, m.BuildCommand)
}

// Contract runs the manifest's contract command with the network OFF.
func (r DockerRunner) Contract(ctx context.Context, dir string, m domain.SampleManifest) StageResult {
	if len(m.ContractCommand) == 0 {
		return skipped("no contract command in manifest")
	}
	return r.stage(ctx, dir, m, true, m.ContractCommand)
}
