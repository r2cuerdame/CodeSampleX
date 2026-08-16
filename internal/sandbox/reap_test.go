package sandbox

import (
	"context"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"strings"
	"testing"
)

// A stage that outruns its timeout must not leave its container running.
//
// The timeout cancels the context, which kills the `docker run` CLIENT.
// The container belongs to dockerd and keeps going, and --rm only removes
// it once it exits — which for a hung build is never. One was found still
// compiling fifteen minutes into a five-minute stage, on a machine where
// six containers is the measured ceiling, so every timed-out verification
// took a permanent share of the pool.
func TestATimedOutStageKillsItsContainer(t *testing.T) {
	var ran [][]string
	old := execCombined
	defer func() { execCombined = old }()
	execCombined = func(ctx context.Context, dir string, argv []string) ([]byte, error) {
		ran = append(ran, argv)
		if argv[1] == "run" {
			return nil, context.DeadlineExceeded
		}
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", Runtime: "node", Language: "javascript"}}
	DockerRunner{}.stage(ctx, t.TempDir(), m, true, []string{"node", "test/contract.mjs"})

	var name string
	for i, a := range ran[0] {
		if a == "--name" && i+1 < len(ran[0]) {
			name = ran[0][i+1]
		}
	}
	if name == "" {
		t.Fatal("the container was started with no name, so nothing can kill it")
	}
	if !strings.HasPrefix(name, "csx-") {
		t.Errorf("container name %q does not identify this project", name)
	}
	var killed bool
	for _, argv := range ran[1:] {
		if len(argv) >= 3 && argv[1] == "kill" && argv[2] == name {
			killed = true
		}
	}
	if !killed {
		t.Errorf("no `docker kill %s` after the stage gave up; the container "+
			"keeps a share of the pool for as long as it runs", name)
	}
}

// Two stages of one sample must not collide, or reaping the second kills
// the first.
func TestContainerNamesAreDistinctPerStage(t *testing.T) {
	dir := t.TempDir()
	resolve := containerName(dir, false, []string{"npm", "ci"})
	contract := containerName(dir, true, []string{"node", "test/contract.mjs"})
	if resolve == contract {
		t.Errorf("both stages named %q", resolve)
	}
	if containerName(dir, true, []string{"node", "x"}) == containerName(dir+"2", true, []string{"node", "x"}) {
		t.Error("two workspaces produced the same container name")
	}
	// And the same stage of the same workspace is stable, or the kill
	// targets a name that was never started.
	if containerName(dir, false, []string{"npm", "ci"}) != resolve {
		t.Error("the name is not stable for one stage")
	}
}
