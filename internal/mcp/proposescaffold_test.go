package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/samples"
)

// scaffoldedWorkdir builds a real clean-room workspace for tests that need
// the tool to describe one. The tool verifies the scaffold before it says
// anything about it, so a made-up path is no longer a success case.
func scaffoldedWorkdir(t *testing.T) string {
	t.Helper()
	spec := samples.BuildSpec(samples.ScanInputs{
		Goal: "axios upload with progress", Kind: "HOW",
		Packages: []string{"pkg:npm/axios@1.12.0"}, Symbols: []string{"axios.post"}})
	work, err := samples.NewProposalWorkspace(t.TempDir(), spec, domain.EnvironmentFingerprint{})
	if err != nil {
		t.Fatalf("scaffold workspace: %v", err)
	}
	return work
}

// R2C-180: propose_public_sample told every agent "A csx.json manifest
// scaffold already exists. Do not recreate it from memory." and handed back
// a directory with nothing in it. The MCP path only ever called
// samples.NewCleanRoom (since removed), which makes an empty directory; the
// three scaffold files were written by the CLI path alone. An agent that
// obeyed the instruction had nothing to complete and stopped, which is how
// this was found in the field.
func TestProposeWritesTheScaffoldItClaimsExists(t *testing.T) {
	home := t.TempDir()
	_, prompt, workdir, err := propose(context.Background(), home,
		"post a JSON body with axios", []string{"pkg:npm/axios@1.12.0"}, []string{"axios.post"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	for _, name := range []string{"csx.json", "spec.json", "PROMPT.md"} {
		if _, err := os.Stat(filepath.Join(workdir, name)); err != nil {
			t.Errorf("%s missing from the workspace the tool reported as ready: %v", name, err)
		}
	}
	if !strings.Contains(prompt, "csx.json manifest scaffold already exists") {
		t.Fatal("the instructions no longer make the claim the scaffold has to satisfy")
	}
	if err := samples.VerifyProposalWorkspace(workdir); err != nil {
		t.Errorf("workspace does not verify: %v", err)
	}
}

// The two failures read alike as prose and need opposite reactions, so the
// tool has to name which one it hit (R2C-180 requirement 6).
func TestProposeToolDistinguishesBadPackagesFromScaffoldFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"bad purls", errProposePackages, "invalid_packages"},
		{"scaffold", samples.ErrScaffold, "scaffold_failed"},
		{"anything else", errors.New("disk on fire"), "propose_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := emptyDeps()
			deps.Propose = func(context.Context, string, []string, []string) (samples.SanitizedSpec, string, string, error) {
				return samples.SanitizedSpec{}, "", "", tc.err
			}
			c := startServer(t, deps)
			res := callTool(t, c, "propose_public_sample", map[string]any{
				"goal":     "axios upload with progress",
				"packages": []string{"pkg:npm/axios@1.12.0"},
			})
			assertProposeFailedClosed(t, res, tc.want)
		})
	}

	// The purl check runs before anything touches the filesystem, so a
	// caller that passes junk is told that and not sent looking for a disk
	// problem. This goes through the real propose, not a stub.
	if _, _, _, err := propose(context.Background(), t.TempDir(), "goal", []string{"axios"}, nil); err == nil {
		t.Fatal("propose accepted a bare package name as a purl")
	} else if !errors.Is(err, errProposePackages) {
		t.Errorf("bad purl error does not classify as invalid_packages: %v", err)
	} else if errors.Is(err, samples.ErrScaffold) {
		t.Error("bad purls are being reported as a scaffold failure")
	}
}

// A failure must never read as a partial success. The whole reason this bug
// was expensive is that an agent handed a workspace path acts on it.
func TestProposeToolFailsClosedWithoutInvitingAGuess(t *testing.T) {
	deps := emptyDeps()
	deps.Propose = func(context.Context, string, []string, []string) (samples.SanitizedSpec, string, string, error) {
		return samples.SanitizedSpec{}, "", "", samples.ErrScaffold
	}
	c := startServer(t, deps)
	res := callTool(t, c, "propose_public_sample", map[string]any{
		"goal":     "axios upload with progress",
		"packages": []string{"pkg:npm/axios@1.12.0"},
	})
	text := assertProposeFailedClosed(t, res, "scaffold_failed")

	for _, forbidden := range []string{
		"Clean-room workspace ready",
		"csx.json manifest scaffold already exists",
		"Generate the sample",
		"csx sample create",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("a failed proposal still says %q:\n%s", forbidden, text)
		}
	}
	for _, want := range []string{"No clean-room workspace was created", "do not create a csx.json from memory"} {
		if !strings.Contains(text, want) {
			t.Errorf("the failure does not say %q:\n%s", want, text)
		}
	}
}

// The success text is the thing that makes the claim, so it is the thing
// that has to check it. A Propose that returns a path to nothing — a stale
// workspace someone deleted, a half-created one — must not become a reply
// telling an agent the scaffold is there.
func TestProposeToolRefusesToDescribeAWorkspaceItCannotSee(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "sample-vanished")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	deps := emptyDeps()
	deps.Propose = func(_ context.Context, goal string, pkgs, symbols []string) (samples.SanitizedSpec, string, string, error) {
		spec := samples.BuildSpec(samples.ScanInputs{Goal: goal, Kind: "HOW", Packages: pkgs, Symbols: symbols})
		return spec, spec.PromptText(), gone, nil
	}
	c := startServer(t, deps)
	res := callTool(t, c, "propose_public_sample", map[string]any{
		"goal":     "axios upload with progress",
		"packages": []string{"pkg:npm/axios@1.12.0"},
	})
	text := assertProposeFailedClosed(t, res, "scaffold_failed")
	if strings.Contains(text, "csx sample create "+gone) {
		t.Error("the reply still points an agent at an empty workspace")
	}
}

// Missing packages is a caller mistake, not a filesystem one.
func TestProposeToolNamesMissingPackagesAsACallerMistake(t *testing.T) {
	c := startServer(t, emptyDeps())
	res := callTool(t, c, "propose_public_sample", map[string]any{"goal": "axios upload"})
	assertProposeFailedClosed(t, res, "invalid_packages")
}

// The reply an agent acts on has to match the disk, end to end: the real
// propose, the real tool, the real files.
func TestProposeToolReplyMatchesTheFilesystem(t *testing.T) {
	home := t.TempDir()
	deps := emptyDeps()
	deps.Propose = func(ctx context.Context, goal string, pkgs, symbols []string) (samples.SanitizedSpec, string, string, error) {
		return propose(ctx, home, goal, pkgs, symbols)
	}
	c := startServer(t, deps)
	res := callTool(t, c, "propose_public_sample", map[string]any{
		"goal":     "post a JSON body with axios",
		"packages": []string{"pkg:npm/axios@1.12.0"},
		"symbols":  []string{"axios.post"},
	})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("propose failed: %s", toolText(t, res))
	}
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structuredContent: %v", res)
	}
	workdir, _ := sc["workdir"].(string)
	if workdir == "" {
		t.Fatal("structuredContent carries no workdir")
	}
	if err := samples.VerifyProposalWorkspace(workdir); err != nil {
		t.Fatalf("the tool reported a workspace that does not verify: %v", err)
	}
	if text := toolText(t, res); !strings.Contains(text, "csx sample create "+workdir) {
		t.Errorf("the reply does not hand the user the create command for %s:\n%s", workdir, text)
	}

	// Retrying the identical proposal is the case an agent actually hits,
	// and it used to leave one more empty directory behind every time.
	again := callTool(t, c, "propose_public_sample", map[string]any{
		"goal":     "post a JSON body with axios",
		"packages": []string{"pkg:npm/axios@1.12.0"},
		"symbols":  []string{"axios.post"},
	})
	againDir, _ := again["structuredContent"].(map[string]any)["workdir"].(string)
	if againDir != workdir {
		t.Errorf("retry created %s instead of reusing %s", againDir, workdir)
	}
	entries, err := os.ReadDir(samples.WorkspaceBase(home))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("two identical proposals left %d directories behind: %v", len(entries), entries)
	}
}

// assertProposeFailedClosed checks the shape every propose failure has to
// have: an error result, and the reason in structuredContent — which is the
// channel the client renders.
func assertProposeFailedClosed(t *testing.T, res map[string]any, wantCode string) string {
	t.Helper()
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("failure did not come back as isError: %v", res)
	}
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("failure carries no structuredContent: %v", res)
	}
	if sc["error"] != wantCode {
		t.Errorf("structuredContent.error = %v, want %q", sc["error"], wantCode)
	}
	if created, _ := sc["workspaceCreated"].(bool); created {
		t.Error("a failed proposal claims a workspace was created")
	}
	if _, hasWorkdir := sc["workdir"]; hasWorkdir {
		t.Errorf("a failed proposal still hands back a workdir: %v", sc["workdir"])
	}
	return toolText(t, res)
}
