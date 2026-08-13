// Package sandbox runs sample verification stages in isolation
// (goal.md §16). Downloaded samples never run directly on the host:
// with Docker they run in two-phase containers (resolve with network,
// everything after with --network=none); without Docker the native
// fallback resolves and compiles only and honestly SKIPs the contract.
package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// StageResult is one verification stage's outcome plus its local-only log.
type StageResult struct {
	Result string // PASS | FAIL | SKIPPED
	Log    string
}

// StageResult.Result values.
const (
	ResultPass    = "PASS"
	ResultFail    = "FAIL"
	ResultSkipped = "SKIPPED"
)

// Runner executes the three sandbox stages over an unpacked sample dir.
type Runner interface {
	Resolve(ctx context.Context, dir string, m domain.SampleManifest) StageResult
	Build(ctx context.Context, dir string, m domain.SampleManifest) StageResult
	Contract(ctx context.Context, dir string, m domain.SampleManifest) StageResult
	// StageEnvironment describes where the stages actually execute, given
	// the host environment. A receipt must name that environment, not the
	// host: a contract run inside a linux container proves nothing about
	// the Windows machine that started it.
	StageEnvironment(host domain.EnvironmentFingerprint, m domain.SampleManifest) domain.EnvironmentFingerprint
}

// stageTimeout bounds every stage (plan C13: 5m/stage).
const stageTimeout = 5 * time.Minute

// execCombined runs argv in dir and returns combined output. A package
// variable so unit tests can script executions without docker/npm.
var execCombined = func(ctx context.Context, dir string, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// runStage executes argv with the stage timeout and maps the exit status
// onto PASS/FAIL. The full command line and output stay in the local log.
func runStage(ctx context.Context, dir string, argv []string) StageResult {
	ctx, cancel := context.WithTimeout(ctx, stageTimeout)
	defer cancel()
	out, err := execCombined(ctx, dir, argv)
	log := "$ " + strings.Join(argv, " ") + "\n" + strings.TrimSpace(string(out))
	if err != nil {
		return StageResult{Result: ResultFail, Log: log + "\nerror: " + err.Error()}
	}
	return StageResult{Result: ResultPass, Log: log}
}

func skipped(reason string) StageResult {
	return StageResult{Result: ResultSkipped, Log: reason}
}

// resolveCommand is the per-ecosystem dependency resolve step. Lifecycle
// scripts never run (--ignore-scripts / metadata-only fetches).
func resolveCommand(ecosystem string) ([]string, error) {
	switch ecosystem {
	case "npm":
		return []string{"npm", "ci", "--ignore-scripts"}, nil
	case "pypi":
		return []string{"pip", "install", "-r", "requirements.txt", "--no-deps"}, nil
	case "golang":
		return []string{"go", "mod", "download"}, nil
	case "cargo":
		return []string{"cargo", "fetch"}, nil
	}
	return nil, fmt.Errorf("sandbox: unsupported ecosystem %q", ecosystem)
}
