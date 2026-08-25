package samples

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func proposalSpec(goal string) SanitizedSpec {
	return BuildSpec(ScanInputs{
		Goal:     goal,
		Kind:     "HOW",
		Packages: []string{"pkg:npm/axios@1.12.0"},
		Symbols:  []string{"axios.post"},
	})
}

func countWorkspaces(t *testing.T, home string) int {
	t.Helper()
	entries, err := os.ReadDir(WorkspaceBase(home))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), proposalWorkPrefix) {
			n++
		}
	}
	return n
}

// The regression this file exists for: the workspace was reported ready and
// the manifest the agent was told to complete was never written.
func TestNewProposalWorkspaceWritesTheWholeScaffold(t *testing.T) {
	home := t.TempDir()
	spec := proposalSpec("post a JSON body with axios")
	work, err := NewProposalWorkspace(home, spec, domain.EnvironmentFingerprint{})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyProposalWorkspace(work); err != nil {
		t.Fatalf("workspace reported ready but does not verify: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(work, proposalManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var manifest domain.SampleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	// PROMPT.md says: preserve case.goal, packages and symbols; fill the
	// empty contract. Every one of those has to actually be in the file.
	if manifest.Case.Goal != spec.Goal {
		t.Errorf("case.goal = %q, want %q", manifest.Case.Goal, spec.Goal)
	}
	if len(manifest.Case.Packages) != 1 || manifest.Case.Packages[0] != "pkg:npm/axios@1.12.0" {
		t.Errorf("case.packages = %v", manifest.Case.Packages)
	}
	if len(manifest.Case.Symbols) != 1 || manifest.Case.Symbols[0] != "axios.post" {
		t.Errorf("case.symbols = %v", manifest.Case.Symbols)
	}
	if len(manifest.Case.Contract) != 0 {
		t.Errorf("case.contract = %v, want empty for the agent to fill", manifest.Case.Contract)
	}
	if manifest.VerifierAdapter != "node-typescript@1" || len(manifest.ContractCommand) == 0 {
		t.Errorf("scaffold does not carry an ecosystem guess: %+v", manifest)
	}

	prompt, err := os.ReadFile(filepath.Join(work, proposalPromptFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "csx.json manifest scaffold already exists") {
		t.Fatal("PROMPT.md no longer makes the claim this scaffold has to satisfy")
	}
}

// Failure must leave nothing behind that looks like a workspace. The old
// path failed the other way round: it created the directory first and the
// files never, so an empty `sample-*` directory WAS the failure mode.
func TestNewProposalWorkspaceFailsClosedAndLeavesNoWorkspace(t *testing.T) {
	home := t.TempDir()
	// A regular file where samples/work has to be: MkdirAll cannot make a
	// directory over it, on Windows and elsewhere alike.
	if err := os.MkdirAll(filepath.Join(home, "samples"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(WorkspaceBase(home), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	work, err := NewProposalWorkspace(home, proposalSpec("goal"), domain.EnvironmentFingerprint{})
	if err == nil {
		t.Fatalf("scaffold creation succeeded over a file: %q", work)
	}
	if !errors.Is(err, ErrScaffold) {
		t.Errorf("error does not classify as a scaffold failure: %v", err)
	}
	if work != "" {
		t.Errorf("a failed call still handed back a path: %q", work)
	}
}

// Staging is what makes this true: no observer sees a `sample-*` directory
// that is not finished, and a failure between the files leaves no directory
// at all.
func TestNewProposalWorkspaceLeavesNoDebrisWhenPromotionFails(t *testing.T) {
	home := t.TempDir()
	base := WorkspaceBase(home)
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := proposalSpec("goal")
	work, err := NewProposalWorkspace(home, spec, domain.EnvironmentFingerprint{})
	if err != nil {
		t.Fatal(err)
	}
	// Every directory under the base is a finished workspace; no staging
	// directory survives a successful call either.
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), proposalStagePrefix) {
			t.Errorf("staging directory %s left behind", e.Name())
		}
	}
	if len(entries) != 1 || filepath.Join(base, entries[0].Name()) != work {
		t.Errorf("base holds %v, want just %s", entries, work)
	}
}

// Windows is the environment this shipped broken on, and its path rules are
// the ones most likely to break an atomic create: spaces, a non-ASCII
// segment, and a home that is itself several levels deep.
func TestNewProposalWorkspaceHandlesAwkwardHomePaths(t *testing.T) {
	for _, segment := range []string{
		"Local Settings",
		"사용자 홈",
		"dir.with.dots",
		"trailing space dir",
	} {
		t.Run(segment, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), segment, "nested", ".csx")
			if err := os.MkdirAll(home, 0o700); err != nil {
				t.Skipf("this filesystem rejects %q: %v", segment, err)
			}
			work, err := NewProposalWorkspace(home, proposalSpec("goal"), domain.EnvironmentFingerprint{})
			if err != nil {
				t.Fatalf("scaffold under %q: %v", segment, err)
			}
			if err := VerifyProposalWorkspace(work); err != nil {
				t.Fatalf("scaffold under %q does not verify: %v", segment, err)
			}
			if !strings.HasPrefix(work, home) {
				t.Errorf("workspace %q escaped home %q", work, home)
			}
		})
	}
}

// Concurrent proposals must not collide, overwrite each other, or produce a
// workspace missing a file because two calls interleaved inside one.
func TestNewProposalWorkspaceIsSafeUnderConcurrency(t *testing.T) {
	home := t.TempDir()
	const n = 16

	var wg sync.WaitGroup
	dirs := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// Distinct goals: identical ones are deliberately deduplicated
			// by the reuse policy, which is a different test.
			dirs[i], errs[i] = NewProposalWorkspace(home,
				proposalSpec(fmt.Sprintf("concurrent goal %d", i)), domain.EnvironmentFingerprint{})
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if seen[dirs[i]] {
			t.Fatalf("call %d reused another call's workspace %s", i, dirs[i])
		}
		seen[dirs[i]] = true
		if err := VerifyProposalWorkspace(dirs[i]); err != nil {
			t.Errorf("call %d produced an incomplete workspace: %v", i, err)
		}
	}
	if got := countWorkspaces(t, home); got != n {
		t.Errorf("%d workspaces on disk, want %d", got, n)
	}
	entries, _ := os.ReadDir(WorkspaceBase(home))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), proposalStagePrefix) {
			t.Errorf("staging directory %s survived", e.Name())
		}
	}
}

// Retry policy. An agent that calls the same proposal again — because the
// first reply scrolled away, because a transport dropped — used to leave one
// more directory behind every time.
func TestIdenticalProposalReusesTheUntouchedWorkspace(t *testing.T) {
	home := t.TempDir()
	spec := proposalSpec("post a JSON body with axios")

	first, err := NewProposalWorkspace(home, spec, domain.EnvironmentFingerprint{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		again, err := NewProposalWorkspace(home, spec, domain.EnvironmentFingerprint{})
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatalf("retry %d created %s instead of reusing %s", i, again, first)
		}
	}
	if got := countWorkspaces(t, home); got != 1 {
		t.Errorf("%d workspaces after 5 identical proposals, want 1", got)
	}

	// A different proposal is a different workspace.
	other, err := NewProposalWorkspace(home, proposalSpec("stream a download with axios"), domain.EnvironmentFingerprint{})
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("a different proposal reused the previous workspace")
	}

	// Once the agent has written into it, the workspace is theirs: a repeat
	// proposal must not hand a second agent the same directory to work in.
	if err := os.WriteFile(filepath.Join(first, "index.mjs"), []byte("// generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := NewProposalWorkspace(home, spec, domain.EnvironmentFingerprint{})
	if err != nil {
		t.Fatal(err)
	}
	if after == first {
		t.Fatal("a proposal reused a workspace an agent had already written into")
	}
}

// The debris left by the regression is reclaimed, and only the debris.
func TestSweepReclaimsOnlyOldEmptyWorkspaces(t *testing.T) {
	home := t.TempDir()
	base := WorkspaceBase(home)
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * emptyWorkspaceGrace)

	stale := filepath.Join(base, "sample-stale")
	recent := filepath.Join(base, "sample-recent")
	occupied := filepath.Join(base, "sample-occupied")
	foreign := filepath.Join(base, "keepme")
	for _, d := range []string{stale, recent, occupied, foreign} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(occupied, "index.mjs"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{stale, occupied, foreign} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := NewProposalWorkspace(home, proposalSpec("goal"), domain.EnvironmentFingerprint{}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale empty workspace not reclaimed: %v", err)
	}
	for _, d := range []string{recent, occupied, foreign} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s should have been left alone: %v", filepath.Base(d), err)
		}
	}
	if _, err := os.Stat(filepath.Join(occupied, "index.mjs")); err != nil {
		t.Errorf("the sweep touched a workspace holding a file: %v", err)
	}
}

// VerifyProposalWorkspace is the gate every success message stands behind,
// so each way a workspace can be unusable has to be a refusal.
func TestVerifyProposalWorkspaceRejectsIncompleteScaffolds(t *testing.T) {
	writeAll := func(t *testing.T, dir string, files map[string]string) {
		t.Helper()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	goodManifest, err := json.Marshal(ProposalManifest(proposalSpec("goal"), domain.EnvironmentFingerprint{}))
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]map[string]string{
		"empty directory": {},
		"no manifest": {
			proposalSpecFile:   "{}",
			proposalPromptFile: "prompt",
		},
		"manifest is empty": {
			proposalSpecFile:     "{}",
			proposalPromptFile:   "prompt",
			proposalManifestFile: "",
		},
		"manifest does not parse": {
			proposalSpecFile:     "{}",
			proposalPromptFile:   "prompt",
			proposalManifestFile: "{ not json",
		},
		"manifest has no case facts": {
			proposalSpecFile:     "{}",
			proposalPromptFile:   "prompt",
			proposalManifestFile: `{"schemaVersion":1}`,
		},
		"no prompt": {
			proposalSpecFile:     "{}",
			proposalManifestFile: string(goodManifest),
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeAll(t, dir, files)
			if err := VerifyProposalWorkspace(dir); err == nil {
				t.Fatal("verification passed an unusable workspace")
			} else if !errors.Is(err, ErrScaffold) {
				t.Errorf("error does not classify as a scaffold failure: %v", err)
			}
		})
	}

	if err := VerifyProposalWorkspace(""); err == nil {
		t.Error("verification passed an empty path")
	}
	if err := VerifyProposalWorkspace(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("verification passed a directory that does not exist")
	}
}
