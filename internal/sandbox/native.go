package sandbox

import (
	"context"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// NativeRunner is the COMPILE_ONLY fallback for hosts without Docker.
// Resolve and Build run natively (still with --ignore-scripts semantics
// where the tool supports it); Contract NEVER runs sample code on the
// host and returns an honest SKIPPED (goal.md §16.3).
type NativeRunner struct{}

// Resolve fetches dependencies natively without lifecycle scripts.
func (NativeRunner) Resolve(ctx context.Context, dir string, m domain.SampleManifest) StageResult {
	cmd, err := resolveCommand(m.Environment.Ecosystem)
	if err != nil {
		return StageResult{Result: ResultFail, Log: err.Error()}
	}
	return runStage(ctx, dir, cmd)
}

// Build runs the manifest's build command natively (typecheck/compile only).
func (NativeRunner) Build(ctx context.Context, dir string, m domain.SampleManifest) StageResult {
	if len(m.BuildCommand) == 0 {
		return skipped("no build command in manifest")
	}
	return runStage(ctx, dir, m.BuildCommand)
}

// Contract is never executed natively: without container isolation the
// receipt must say SKIPPED rather than pretend verification happened.
func (NativeRunner) Contract(ctx context.Context, dir string, m domain.SampleManifest) StageResult {
	return skipped("contract skipped: COMPILE_ONLY capability — sample code does not run on the host")
}
