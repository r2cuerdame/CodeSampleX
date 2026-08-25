package samples

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The three files every clean-room workspace carries before anybody is told
// it exists. PROMPT.md tells the agent "A csx.json manifest scaffold already
// exists", so csx.json existing is not a nicety — it is the precondition for
// the instruction the agent is about to be given.
const (
	proposalSpecFile     = "spec.json"
	proposalPromptFile   = "PROMPT.md"
	proposalManifestFile = "csx.json"
)

// ErrScaffold marks every failure to build a clean-room workspace. It exists
// so a caller can tell "your packages were not purls" apart from "this
// machine could not create the workspace" — two failures an agent must
// react to differently, and which used to arrive as the same opaque string.
var ErrScaffold = errors.New("clean-room scaffold")

// proposalWorkPrefix names a finished workspace; proposalStagePrefix names
// one still being written. The two prefixes are what makes creation atomic:
// files are written under the staging name and the directory is renamed into
// its final name only once every file is on disk and verified. Nothing ever
// observes a `sample-*` directory in a half-built state, and a failure
// leaves no `sample-*` directory at all.
const (
	proposalWorkPrefix  = "sample-"
	proposalStagePrefix = ".staging-"
)

// emptyWorkspaceGrace is how long an empty `sample-*` directory is left
// alone before the sweep reclaims it. Nothing this package writes can be
// observed empty, so an empty directory is either debris from the regression
// this policy exists to bound, or a directory another process created
// moments ago — the grace period is what keeps the sweep off the second.
const emptyWorkspaceGrace = time.Hour

// maxSweep bounds one sweep so a home with thousands of stale directories
// does not turn a proposal into a long filesystem walk.
const maxSweep = 512

// WorkspaceBase is the directory holding every clean-room workspace.
func WorkspaceBase(home string) string {
	return filepath.Join(home, "samples", "work")
}

// NewProposalWorkspace creates the clean-room workspace for one proposal and
// fills it with the scaffold the generation instructions promise: spec.json,
// PROMPT.md and the csx.json manifest with an empty contract for the agent to
// complete.
//
// It returns only workspaces that exist and are complete. The files are
// written under a staging name and the directory is renamed into place, so
// the returned path is either a fully scaffolded workspace or the call
// failed — there is no third outcome, and in particular there is no
// successful call that returns an empty directory.
//
// Retry policy: an identical proposal whose workspace is still untouched —
// the three scaffold files and nothing else — is handed back rather than
// duplicated, so an agent retrying the same call does not leave a new
// directory behind each time.
func NewProposalWorkspace(home string, spec SanitizedSpec, fp domain.EnvironmentFingerprint) (string, error) {
	specJSON, promptText, manifestJSON, err := proposalScaffold(spec, fp)
	if err != nil {
		return "", err
	}

	base := WorkspaceBase(home)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("samples: %w: %w", ErrScaffold, err)
	}

	// Reclaiming the debris and reusing an identical proposal are the two
	// halves of bounding growth; both read the same directory listing.
	entries, _ := os.ReadDir(base)
	if reused, ok := reusableWorkspace(base, entries, specJSON); ok {
		return reused, nil
	}
	sweepEmptyWorkspaces(base, entries)

	files := map[string][]byte{
		proposalSpecFile:     specJSON,
		proposalPromptFile:   []byte(promptText),
		proposalManifestFile: manifestJSON,
	}

	// A name collision with a pre-existing workspace is astronomically
	// unlikely and silently destructive if it ever happened, so it is
	// retried rather than assumed away.
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		workdir, err := stageAndPromote(base, files)
		if err == nil {
			return workdir, nil
		}
		if !errors.Is(err, errWorkspaceNameTaken) {
			return "", err
		}
		lastErr = err
	}
	return "", fmt.Errorf("samples: %w: could not reserve a workspace name: %w", ErrScaffold, lastErr)
}

var errWorkspaceNameTaken = errors.New("workspace name taken")

// stageAndPromote writes the whole scaffold into a staging directory and
// renames it into its final name. os.MkdirTemp is what makes concurrent
// callers safe: it creates the staging directory exclusively, so the
// suffix it picked belongs to this call alone and the final name derived
// from it cannot be raced onto by another proposal.
func stageAndPromote(base string, files map[string][]byte) (string, error) {
	staging, err := os.MkdirTemp(base, proposalStagePrefix+"*")
	if err != nil {
		return "", fmt.Errorf("samples: %w: %w", ErrScaffold, err)
	}
	workdir := filepath.Join(base, proposalWorkPrefix+strings.TrimPrefix(filepath.Base(staging), proposalStagePrefix))

	// Anything below here must not leave the staging directory behind: a
	// half-written workspace nobody can see is the point of staging, and a
	// half-written workspace nobody cleans up is just a differently named
	// version of the leak this fixes.
	promoted := false
	defer func() {
		if !promoted {
			os.RemoveAll(staging)
		}
	}()

	if _, err := os.Lstat(workdir); err == nil {
		return "", errWorkspaceNameTaken
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(staging, name), files[name], 0o644); err != nil {
			return "", fmt.Errorf("samples: %w: write %s: %w", ErrScaffold, name, err)
		}
	}
	// Verified where it can still be thrown away. The promise this call
	// makes is about the files, not about WriteFile returning nil.
	if err := VerifyProposalWorkspace(staging); err != nil {
		return "", err
	}

	if err := os.Rename(staging, workdir); err != nil {
		// On Windows a rename loses to any process holding a handle inside
		// the tree — an indexer, a virus scanner, the agent's own editor.
		// That is a retryable name problem, not a corrupt scaffold.
		if _, statErr := os.Lstat(workdir); statErr == nil {
			return "", errWorkspaceNameTaken
		}
		return "", fmt.Errorf("samples: %w: promote workspace: %w", ErrScaffold, err)
	}
	promoted = true

	// The claim is about the path the caller is handed, so it is checked at
	// that path rather than inferred from the staging directory it used to be.
	if err := VerifyProposalWorkspace(workdir); err != nil {
		return "", err
	}
	return workdir, nil
}

// VerifyProposalWorkspace reports whether dir really holds a usable
// clean-room scaffold. It is the check standing between a scaffold and any
// message telling an agent that a scaffold is there.
func VerifyProposalWorkspace(dir string) error {
	if dir == "" {
		return fmt.Errorf("samples: %w: no workspace path", ErrScaffold)
	}
	for _, name := range []string{proposalSpecFile, proposalPromptFile, proposalManifestFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("samples: %w: %s missing from %s: %w", ErrScaffold, name, dir, err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("samples: %w: %s in %s is empty", ErrScaffold, name, dir)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, proposalManifestFile))
	if err != nil {
		return fmt.Errorf("samples: %w: read %s: %w", ErrScaffold, proposalManifestFile, err)
	}
	var manifest domain.SampleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("samples: %w: %s in %s does not parse: %w", ErrScaffold, proposalManifestFile, dir, err)
	}
	// An agent is told to preserve case.goal, packages and symbols rather
	// than recreate them. A scaffold missing either is one it would have to
	// invent from memory, which is the failure mode this whole path exists
	// to prevent.
	if strings.TrimSpace(manifest.Case.Goal) == "" || len(manifest.Case.Packages) == 0 {
		return fmt.Errorf("samples: %w: %s in %s carries no goal or packages", ErrScaffold, proposalManifestFile, dir)
	}
	return nil
}

// proposalScaffold renders the three scaffold files for one spec.
func proposalScaffold(spec SanitizedSpec, fp domain.EnvironmentFingerprint) (specJSON []byte, prompt string, manifestJSON []byte, err error) {
	specJSON, err = json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, "", nil, fmt.Errorf("samples: %w: %w", ErrScaffold, err)
	}
	specJSON = append(specJSON, '\n')
	manifestJSON, err = json.MarshalIndent(ProposalManifest(spec, fp), "", "  ")
	if err != nil {
		return nil, "", nil, fmt.Errorf("samples: %w: %w", ErrScaffold, err)
	}
	manifestJSON = append(manifestJSON, '\n')
	return specJSON, spec.PromptText(), manifestJSON, nil
}

// reusableWorkspace finds a workspace for exactly this proposal that the
// agent has not written anything into yet. Returning it instead of a new
// directory is what keeps a retried proposal from leaving one more
// workspace behind every time it is retried.
//
// "Untouched" is deliberately strict: the moment any file beyond the
// scaffold appears, the workspace belongs to whoever wrote it and a fresh
// one is created instead.
func reusableWorkspace(base string, entries []os.DirEntry, specJSON []byte) (string, bool) {
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), proposalWorkPrefix) {
			continue
		}
		dir := filepath.Join(base, e.Name())
		inner, err := os.ReadDir(dir)
		if err != nil || len(inner) != 3 {
			continue
		}
		untouched := true
		for _, f := range inner {
			switch f.Name() {
			case proposalSpecFile, proposalPromptFile, proposalManifestFile:
			default:
				untouched = false
			}
		}
		if !untouched {
			continue
		}
		got, err := os.ReadFile(filepath.Join(dir, proposalSpecFile))
		if err != nil || string(got) != string(specJSON) {
			continue
		}
		if VerifyProposalWorkspace(dir) != nil {
			continue
		}
		return dir, true
	}
	return "", false
}

// sweepEmptyWorkspaces reclaims empty `sample-*` directories. Nothing this
// package creates can be observed empty, so these are the leftovers of the
// regression that returned a workspace before writing anything into it.
// Only directories with no entries at all are removed, so no file can be
// lost by it, and the grace period keeps it away from anything another
// process created moments ago.
func sweepEmptyWorkspaces(base string, entries []os.DirEntry) {
	cutoff := time.Now().Add(-emptyWorkspaceGrace)
	removed := 0
	for _, e := range entries {
		if removed >= maxSweep {
			return
		}
		if !e.IsDir() || !strings.HasPrefix(e.Name(), proposalWorkPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		dir := filepath.Join(base, e.Name())
		inner, err := os.ReadDir(dir)
		if err != nil || len(inner) != 0 {
			continue
		}
		// os.Remove, never RemoveAll: it refuses on a non-empty directory,
		// so a file that appeared between the listing and here survives.
		if os.Remove(dir) == nil {
			removed++
		}
	}
}

// ProposalManifest builds the csx.json scaffold for a proposal: the case
// facts the agent must preserve, and the environment, commands and adapter
// it is asked to correct for the files it actually generates. Case.Contract
// is deliberately empty — the assertions are the agent's work.
func ProposalManifest(spec SanitizedSpec, fp domain.EnvironmentFingerprint) domain.SampleManifest {
	if fp.Ecosystem == "" && len(spec.Packages) > 0 {
		if parsed, err := domain.ParsePURL(spec.Packages[0]); err == nil {
			fp.Ecosystem = parsed.Ecosystem
		}
	}
	if fp.SchemaVersion == 0 {
		fp.SchemaVersion = 1
	}
	if fp.PackageManager == "" {
		fp.PackageManager = map[string]string{
			"npm": "npm", "pypi": "pip", "golang": "go", "cargo": "cargo",
			"composer": "composer", "gem": "bundler", "pub": "pub", "hex": "mix", "maven": "maven",
		}[fp.Ecosystem]
	}
	kind := spec.Kind
	if kind == "" {
		kind = "HOW"
	}
	return domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1,
			Kind:          kind,
			Goal:          spec.Goal,
			Packages:      append([]string(nil), spec.Packages...),
			Symbols:       append([]string(nil), spec.Symbols...),
			Constraints:   spec.Constraints,
			Contract:      []string{},
		},
		Packages: append([]string(nil), spec.Packages...),
		Symbols:  append([]string(nil), spec.Symbols...),
		// One package named means the sample is about that package, and the
		// authoring queue assigns exactly one. Several means nobody can say
		// which is the subject, and naming the first would be a guess wearing
		// a fact's clothes — the snapshot's narrowest-claim inference exists
		// for that case and is honest about being an inference.
		Subject:         proposalSubject(spec.Packages),
		Environment:     fp.Normalize(),
		License:         DefaultLicense,
		ContractCommand: proposalContractCommand(fp.Ecosystem),
		VerifierAdapter: proposalVerifierAdapter(fp),
	}
}

// proposalSubject names the one package a sample is about, or nothing.
func proposalSubject(packages []string) string {
	if len(packages) == 1 {
		return packages[0]
	}
	return ""
}

func proposalVerifierAdapter(fp domain.EnvironmentFingerprint) string {
	if strings.EqualFold(strings.TrimSpace(fp.Ecosystem), "maven") && strings.EqualFold(strings.TrimSpace(fp.PackageManager), "gradle") {
		return "gradle-java@1"
	}
	return map[string]string{
		"npm": "node-typescript@1", "pypi": "python@1", "golang": "golang@1",
		"cargo": "cargo@1", "composer": "composer@1", "gem": "gem@1",
		"pub": "pub@1", "hex": "hex@1", "maven": "maven-java@1",
	}[strings.ToLower(strings.TrimSpace(fp.Ecosystem))]
}

func proposalContractCommand(ecosystem string) []string {
	commands := map[string][]string{
		"npm": {"node", "test/contract.mjs"}, "pypi": {"python", "test/contract.py"},
		"golang": {"go", "run", "./test"}, "cargo": {"cargo", "run", "--offline"},
		"composer": {"php", "test/contract.php"}, "gem": {"bundle", "exec", "ruby", "test/contract.rb"},
		"pub": {"dart", "test", "--reporter=expanded"}, "hex": {"mix", "test", "--no-deps-check"},
	}
	return append([]string(nil), commands[strings.ToLower(strings.TrimSpace(ecosystem))]...)
}
