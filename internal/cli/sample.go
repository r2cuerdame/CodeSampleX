package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/search"
	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
	"github.com/r2cuerdame/codesamplex/internal/verifier"
)

// llmTimeout bounds a configured llmCommand generation run (plan P8.3).
const llmTimeout = 10 * time.Minute

// Test seams. Production code never reassigns these; tests in this
// package swap them to capture output, script stdin, and fake the
// verification sandbox (same unexported-var pattern as internal/sandbox).
var (
	sampleStdout io.Writer = os.Stdout
	sampleStderr io.Writer = os.Stderr
	sampleStdin  io.Reader = os.Stdin

	// verifierRunner overrides the capability-derived sandbox runner.
	verifierRunner sandbox.Runner
	// verifierCapability overrides sandbox.Detect for `csx sample verify`.
	verifierCapability domain.SandboxCapability

	sampleHTTP = &http.Client{Timeout: 60 * time.Second}
)

func init() {
	Register(Command{
		Name:    "sample",
		Summary: "clean-room public sample workflow: propose|create|preview|verify|publish|remove|list",
		Run:     sampleMain,
	})
}

func sampleUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: csx sample <subcommand>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  propose --goal G [--package purl]... [--symbol s]...")
	fmt.Fprintln(w, "          build a sanitized spec and clean-room workspace for LLM generation")
	fmt.Fprintln(w, "  create <dir>        turn a clean-room directory (with csx.json) into a local sample")
	fmt.Fprintln(w, "  preview <sampleId>  show EVERYTHING that would be published — nothing hidden")
	fmt.Fprintln(w, "  verify <sampleId> [--json]")
	fmt.Fprintln(w, "                       run verification, save the receipt, optionally print only receipt JSON")
	fmt.Fprintln(w, "  publish <sampleId> [--seeder name | --anonymous] [--server URL]")
	fmt.Fprintln(w, "          upload after leakage re-scan and explicit typed approval")
	fmt.Fprintln(w, "  remove <fullSampleId>")
	fmt.Fprintln(w, "          remove one exact local sample after typing its full id again")
	fmt.Fprintln(w, "  list                list local samples")
	fmt.Fprintln(w, "  pending             samples an agent prepared that nobody has reviewed yet")
}

func sampleMain(ctx context.Context, args []string) int {
	if len(args) == 0 {
		sampleUsage(sampleStderr)
		return 2
	}
	switch args[0] {
	case "propose":
		return samplePropose(ctx, args[1:])
	case "create":
		return sampleCreate(ctx, args[1:])
	case "preview":
		return samplePreview(ctx, args[1:])
	case "verify":
		return sampleVerify(ctx, args[1:])
	case "publish":
		return samplePublish(ctx, args[1:])
	case "remove":
		return sampleRemove(ctx, args[1:])
	case "list":
		return sampleList(ctx, args[1:])
	case "pending":
		return samplePending(ctx, args[1:])
	default:
		fmt.Fprintf(sampleStderr, "csx sample: unknown subcommand %q\n\n", args[0])
		sampleUsage(sampleStderr)
		return 2
	}
}

// exactSampleID accepts only the complete canonical content address. Removal
// intentionally does not inherit resolveLocalSample's prefix convenience:
// destructive commands must identify exactly one immutable artifact.
func exactSampleID(id string) bool {
	const prefix = "sha256:"
	if len(id) != len(prefix)+64 || !strings.HasPrefix(id, prefix) {
		return false
	}
	for _, c := range id[len(prefix):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// multiStringFlag collects a repeatable string flag.
type multiStringFlag []string

func (m *multiStringFlag) String() string { return strings.Join(*m, ",") }
func (m *multiStringFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// sampleEnv is the shared local state every sample subcommand needs.
type sampleEnv struct {
	home string
	cfg  *config.Config
	db   *localdb.DB
	cas  *cas.Store
}

func openSampleEnv() (*sampleEnv, error) {
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	if err := config.EnsureHome(home); err != nil {
		return nil, err
	}
	cfg, err := config.Load(home)
	if err != nil {
		return nil, err
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		return nil, err
	}
	store, err := cas.Open(filepath.Join(home, "cas"))
	if err != nil {
		db.Close()
		return nil, err
	}
	return &sampleEnv{home: home, cfg: cfg, db: db, cas: store}, nil
}

func (e *sampleEnv) Close() {
	if e.db != nil {
		e.db.Close()
	}
}

// resolveLocalSample finds one local sample by full id or unique prefix
// (with or without the "sha256:" prefix).
func resolveLocalSample(ctx context.Context, db *localdb.DB, arg string) (localdb.SampleRow, error) {
	if row, ok, err := db.GetSample(ctx, arg); err != nil {
		return localdb.SampleRow{}, err
	} else if ok {
		return row, nil
	}
	rows, err := db.ListSamples(ctx)
	if err != nil {
		return localdb.SampleRow{}, err
	}
	var matches []localdb.SampleRow
	for _, r := range rows {
		bare := strings.TrimPrefix(r.SampleID, "sha256:")
		if strings.HasPrefix(r.SampleID, arg) || strings.HasPrefix(bare, strings.TrimPrefix(arg, "sha256:")) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return localdb.SampleRow{}, fmt.Errorf("no local sample matches %q (try `csx sample list`)", arg)
	default:
		return localdb.SampleRow{}, fmt.Errorf("%q is ambiguous: %d local samples match", arg, len(matches))
	}
}

// readSampleArtifact loads the canonical artifact bytes for a sample from
// the CAS and re-verifies the content hash BEFORE anything unpacks or
// uploads it.
func readSampleArtifact(store *cas.Store, sampleID string) ([]byte, error) {
	rc, err := store.Get(sampleID)
	if err != nil {
		return nil, fmt.Errorf("artifact for %s not in local cache: %w", sampleID, err)
	}
	defer rc.Close()
	tgz, err := io.ReadAll(io.LimitReader(rc, samples.MaxCompressedBytes+1))
	if err != nil {
		return nil, err
	}
	if got := domain.SHA256Hex(tgz); got != sampleID {
		return nil, fmt.Errorf("artifact hash mismatch: cached content is %s, expected %s", got, sampleID)
	}
	return tgz, nil
}

// unpackToTemp unpacks an artifact into a fresh temp dir; the caller must
// os.RemoveAll the returned dir.
func unpackToTemp(tgz []byte) (string, error) {
	dir, err := os.MkdirTemp("", "csx-sample-*")
	if err != nil {
		return "", err
	}
	if err := samples.Unpack(tgz, dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// listSampleFiles returns the sorted slash-relative paths of every regular
// file under dir.
func listSampleFiles(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func printFindings(w io.Writer, findings []samples.Finding) {
	for _, f := range findings {
		fmt.Fprintf(w, "  %s:%d  %s  %s\n", f.File, f.Line, f.Kind, f.Excerpt)
	}
}

// --- propose -----------------------------------------------------------

func samplePropose(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("sample propose", flag.ContinueOnError)
	fs.SetOutput(sampleStderr)
	goal := fs.String("goal", "", "what the sample should demonstrate (required)")
	var pkgs, syms multiStringFlag
	fs.Var(&pkgs, "package", "public package purl (repeatable); default: public packages of the current project")
	fs.Var(&syms, "symbol", "symbol/API family to demonstrate (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *goal == "" {
		fmt.Fprintln(sampleStderr, "csx sample propose: --goal is required")
		return 2
	}

	env, err := openSampleEnv()
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample propose: %v\n", err)
		return 1
	}
	defer env.Close()

	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}

	// Publicness checks only run in community mode (same gate as
	// `csx run`); anywhere else everything stays UNKNOWN = excluded.
	var checker *registry.Checker
	if config.MayContactRegistries(env.cfg.Mode) {
		checker = &registry.Checker{Cache: evidence.PublicnessCache{DB: env.db}}
	}
	res, _ := evidence.Scan(ctx, dir, checker)

	var fp domain.EnvironmentFingerprint
	if res != nil {
		fp = res.Env
	} else {
		fp = environment.Collect(ctx, nil)
	}

	packages := []string(pkgs)
	symbols := []string(syms)
	if len(packages) == 0 && res != nil {
		for _, p := range res.Packages {
			if p.Publicness == scanner.PublicnessPublic {
				packages = append(packages, p.PURL.String())
			}
		}
		if len(symbols) == 0 {
			for _, s := range res.Symbols {
				symbols = append(symbols, s.Family)
			}
		}
	}
	if len(packages) == 0 {
		fmt.Fprintln(sampleStderr, "csx sample propose: no public packages detected in the current directory;")
		fmt.Fprintln(sampleStderr, "pass them explicitly: csx sample propose --goal ... --package pkg:npm/axios@1.12.0")
		return 1
	}

	spec := samples.BuildSpec(samples.ScanInputs{
		Goal:        *goal,
		Kind:        "HOW",
		Packages:    packages,
		Symbols:     symbols,
		Environment: fp,
	})

	// One implementation, shared with the MCP tool. They used to be two, and
	// only this one wrote the scaffold — which is how `propose_public_sample`
	// came to promise a csx.json that was never there (R2C-180).
	work, err := samples.NewProposalWorkspace(env.home, spec, fp)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample propose: %v\n", err)
		return 1
	}

	fmt.Fprintf(sampleStdout, "Clean-room workspace: %s\n", work)
	fmt.Fprintf(sampleStdout, "Spec: spec.json, manifest scaffold: csx.json, instructions: PROMPT.md (packages: %s)\n", strings.Join(spec.Packages, ", "))

	if env.cfg.LLMCommand != "" {
		// Generation always happens with the USER's local LLM/agent
		// (goal.md §9.3) — never on any server.
		return runLLMCommand(ctx, env.cfg.LLMCommand, work)
	}

	fmt.Fprintln(sampleStdout)
	fmt.Fprintln(sampleStdout, "No llmCommand configured. Next step (goal.md §9.3 — generation is done by")
	fmt.Fprintln(sampleStdout, "YOUR local LLM/agent, never a server):")
	fmt.Fprintf(sampleStdout, "  1. Point your agent/LLM at the workspace and PROMPT.md:\n       %s\n", filepath.Join(work, "PROMPT.md"))
	fmt.Fprintln(sampleStdout, "  2. Let it generate the sample files and complete the existing csx.json scaffold.")
	fmt.Fprintf(sampleStdout, "  3. Then run: csx sample create %s\n", work)
	return 0
}

// runLLMCommand runs the configured local LLM command inside the clean
// room with PROMPT.md piped to stdin and inherited stdio.
func runLLMCommand(ctx context.Context, command, work string) int {
	argv := strings.Fields(command)
	if len(argv) == 0 {
		fmt.Fprintln(sampleStderr, "csx sample propose: llmCommand in config.json is empty after parsing")
		return 1
	}
	prompt, err := os.Open(filepath.Join(work, "PROMPT.md"))
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample propose: %v\n", err)
		return 1
	}
	defer prompt.Close()

	cctx, cancel := context.WithTimeout(ctx, llmTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = work
	cmd.Stdin = prompt
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Fprintf(sampleStdout, "Running llmCommand %q in the clean room (10min timeout)...\n", command)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample propose: llmCommand failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(sampleStdout, "Generation finished. Review the files, then run: csx sample create %s\n", work)
	return 0
}

// --- create ------------------------------------------------------------

func sampleCreate(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(sampleStderr, "usage: csx sample create <dir>")
		return 2
	}
	dir := args[0]
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	raw, err := os.ReadFile(filepath.Join(dir, "csx.json"))
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample create: %s has no csx.json manifest: %v\n", dir, err)
		return 1
	}
	var manifest domain.SampleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample create: parse csx.json: %v\n", err)
		return 1
	}
	// License default MIT-0 (goal.md §7.5); CreateFromDir also enforces it,
	// this keeps the printed summary honest too.
	if manifest.License == "" {
		manifest.License = samples.DefaultLicense
	}

	created, err := samples.CreateFromDir(ctx, dir, manifest)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample create: %v\n", err)
		return 1
	}

	env, err := openSampleEnv()
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample create: %v\n", err)
		return 1
	}
	defer env.Close()

	if _, err := env.cas.Put(bytes.NewReader(created.Artifact)); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample create: store artifact: %v\n", err)
		return 1
	}
	if err := search.SeedSampleDoc(ctx, env.db, created.Manifest, created.SampleID, "LOCAL"); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample create: index sample: %v\n", err)
		return 1
	}
	row, ok, err := env.db.GetSample(ctx, created.SampleID)
	if err != nil || !ok {
		fmt.Fprintf(sampleStderr, "csx sample create: reload sample row: %v\n", err)
		return 1
	}
	row.HasArtifact = true
	if err := env.db.SaveSample(ctx, row); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample create: %v\n", err)
		return 1
	}

	// The proposal has been acted on: it now lives in `csx sample list`,
	// so it should stop being offered as pending work.
	if err := env.db.SetProposalState(ctx, dir, "created"); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample create: note: pending proposal not cleared: %v\n", err)
	}

	fmt.Fprintf(sampleStdout, "Sample created: %s\n", created.SampleID)
	fmt.Fprintf(sampleStdout, "License: %s\n", created.Manifest.License)
	fmt.Fprintf(sampleStdout, "Leakage findings: %d\n", len(created.Findings))
	if len(created.Findings) > 0 {
		printFindings(sampleStdout, created.Findings)
		fmt.Fprintln(sampleStdout, "Findings do not block creation but WILL block publish until fixed.")
	}
	if len(created.Manifest.ContractCommand) == 0 {
		fmt.Fprintf(sampleStdout, "Note: no contractCommand — verification is capped at %s (goal.md §7.5).\n", created.MaxLevel)
	}
	fmt.Fprintf(sampleStdout, "Next: csx sample preview %s\n", created.SampleID)
	return 0
}

// --- preview -----------------------------------------------------------

func samplePreview(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(sampleStderr, "usage: csx sample preview <sampleId>")
		return 2
	}
	env, err := openSampleEnv()
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample preview: %v\n", err)
		return 1
	}
	defer env.Close()

	row, err := resolveLocalSample(ctx, env.db, args[0])
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample preview: %v\n", err)
		return 1
	}
	tgz, err := readSampleArtifact(env.cas, row.SampleID)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample preview: %v\n", err)
		return 1
	}
	dir, err := unpackToTemp(tgz)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample preview: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)

	var manifest domain.SampleManifest
	_ = json.Unmarshal([]byte(row.ManifestJSON), &manifest)

	fmt.Fprintln(sampleStdout, "PUBLISH PREVIEW — everything below is EXACTLY what would be published. Nothing is hidden.")
	fmt.Fprintf(sampleStdout, "Sample:  %s\n", row.SampleID)
	fmt.Fprintf(sampleStdout, "Status:  %s\n", row.Status)
	fmt.Fprintf(sampleStdout, "License: %s\n", manifest.License)
	fmt.Fprintf(sampleStdout, "Case:    [%s] %s\n", manifest.Case.Kind, manifest.Case.Goal)
	for _, c := range manifest.Case.Contract {
		fmt.Fprintf(sampleStdout, "Contract: %s\n", c)
	}
	fmt.Fprintf(sampleStdout, "Packages: %s\n", strings.Join(manifest.Packages, ", "))

	paths, err := listSampleFiles(dir)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample preview: %v\n", err)
		return 1
	}
	fmt.Fprintf(sampleStdout, "\nFiles (%d):\n", len(paths))
	for _, p := range paths {
		fmt.Fprintf(sampleStdout, "  %s\n", p)
	}
	for _, p := range paths {
		content, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if rerr != nil {
			fmt.Fprintf(sampleStderr, "csx sample preview: %v\n", rerr)
			return 1
		}
		fmt.Fprintf(sampleStdout, "\n--- %s (%d bytes) ---\n%s", p, len(content), content)
		if len(content) > 0 && content[len(content)-1] != '\n' {
			fmt.Fprintln(sampleStdout)
		}
	}

	// The SAME options the publish gate will use.
	//
	// Preview says "everything below is EXACTLY what would be published.
	// Nothing is hidden" and then scanned with provenance derived from the
	// throwaway unpack directory -- names like csx-sample-1553973873, which
	// can match nothing. So the project-name check, the entire purpose of
	// provenance.go, could not fire here at all: a contributor working in
	// C:/work/acme-billing saw "Leakage findings: 0" from a preview that
	// swore it showed what publish would do, and then publish REFUSED.
	findings, err := samples.Scan(dir, publishProvenance())
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample preview: leakage scan: %v\n", err)
		return 1
	}
	fmt.Fprintf(sampleStdout, "\nLeakage findings: %d\n", len(findings))
	if len(findings) > 0 {
		printFindings(sampleStdout, findings)
		fmt.Fprintln(sampleStdout, "Publish will be REFUSED until these are fixed (re-create the sample after editing).")
	}
	return 0
}

// --- verify ------------------------------------------------------------

func sampleVerify(ctx context.Context, args []string) int {
	sampleID, jsonOutput, ok := parseSampleVerifyArgs(args)
	if !ok {
		fmt.Fprintln(sampleStderr, "usage: csx sample verify <sampleId> [--json]")
		return 2
	}
	env, err := openSampleEnv()
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample verify: %v\n", err)
		return 1
	}
	defer env.Close()

	row, err := resolveLocalSample(ctx, env.db, sampleID)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample verify: %v\n", err)
		return 1
	}
	tgz, err := readSampleArtifact(env.cas, row.SampleID)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample verify: %v\n", err)
		return 1
	}
	dir, err := unpackToTemp(tgz)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample verify: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)

	raw, err := os.ReadFile(filepath.Join(dir, "csx.json"))
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample verify: sample has no csx.json: %v\n", err)
		return 1
	}
	var manifest domain.SampleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample verify: parse csx.json: %v\n", err)
		return 1
	}

	capability := verifierCapability
	if capability == "" {
		capability = sandbox.Detect(ctx)
	}
	runner := verifierRunner
	if runner == nil {
		runner = sampleRunner(ctx, capability)
	}

	ident, err := identity.LoadOrCreate(env.home)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample verify: %v\n", err)
		return 1
	}
	fp := environment.Collect(ctx, map[string]string{"ecosystem": manifest.Environment.Ecosystem})

	receipt, stageLogs, err := verifier.RunLogged(ctx, runner, capability, dir, manifest, ident, fp)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample verify: %v\n", err)
		return 1
	}
	if receipt.SampleID != row.SampleID {
		fmt.Fprintf(sampleStderr, "csx sample verify: rebuilt sample id %s != %s — refusing to save receipt\n",
			receipt.SampleID, row.SampleID)
		return 1
	}
	if err := env.db.SaveReceipt(ctx, receipt); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample verify: save receipt: %v\n", err)
		return 1
	}
	if receipt.Stages["contract"] == sandbox.ResultPass && row.Status == "LOCAL" {
		_ = env.db.SetSampleStatus(ctx, row.SampleID, "LOCAL_PASS")
	}
	if jsonOutput {
		// Automation must not scrape the human stage table. The receipt is
		// already the bounded, signed, machine-readable record and contains no
		// raw logs or local paths. Keep stdout to exactly one JSON document;
		// operational errors continue to use stderr and a non-zero exit code.
		_, _ = sampleStdout.Write(append(domain.MustCanonicalJSON(receipt), '\n'))
		return 0
	}

	// The heading is what a reader takes away, and "Verified" over a table
	// whose compile line says FAIL is the wrong takeaway. A receipt was
	// produced either way — that is the honest word for it — so the outcome
	// goes in the heading instead of only in the table below.
	switch contract := receipt.Stages["contract"]; contract {
	case sandbox.ResultPass:
		fmt.Fprintf(sampleStdout, "Verified %s — contract PASSED\n", row.SampleID)
	case sandbox.ResultFail:
		fmt.Fprintf(sampleStdout, "Receipt written for %s — contract FAILED\n", row.SampleID)
	default:
		fmt.Fprintf(sampleStdout, "Receipt written for %s — contract did not run (%s)\n",
			row.SampleID, contract)
	}
	fmt.Fprintf(sampleStdout, "Sandbox capability: %s\n", capability)
	fmt.Fprintln(sampleStdout, "Stage      Result")
	for _, stage := range []string{"resolve", "compile", "load", "contract"} {
		fmt.Fprintf(sampleStdout, "%-10s %s\n", stage, receipt.Stages[stage])
	}
	// What actually happened, for the stage that failed.
	//
	// A table of PASS/FAIL and nothing else left an author with no way to
	// see what their own assertion did, inside a container they cannot
	// enter -- the only route was to rebuild it by hand and guess at the
	// flags. This is the loop the whole contribution side depends on.
	//
	// Local only: the receipt carries a digest of these logs and never the
	// logs themselves, because raw output carries paths and usernames.
	for _, stage := range []string{"resolve", "compile", "contract"} {
		if receipt.Stages[stage] != sandbox.ResultFail {
			continue
		}
		if tail := lastLines(stageLogs[stage], failLogLines); tail != "" {
			fmt.Fprintf(sampleStdout, "\nWhat %s printed (last %d lines, this machine only):\n",
				stage, failLogLines)
			fmt.Fprintln(sampleStdout, tail)
		}
		break
	}
	if capability == domain.CapCompileOnly && receipt.Stages["contract"] == sandbox.ResultSkipped {
		fmt.Fprintln(sampleStdout, "Note: no container isolation on this host (COMPILE_ONLY). NOTHING from the")
		fmt.Fprintln(sampleStdout, "sample was executed — not the contract, not the build, not even dependency")
		fmt.Fprintln(sampleStdout, "resolution, because resolving runs setup.py and build.rs from packages the")
		fmt.Fprintln(sampleStdout, "sample chooses. This receipt proves nothing about the sample; it records")
		fmt.Fprintln(sampleStdout, "that this machine could not judge it. Install Docker to verify.")
	}
	return 0
}

func parseSampleVerifyArgs(args []string) (sampleID string, jsonOutput, ok bool) {
	for _, arg := range args {
		switch {
		case arg == "--json":
			if jsonOutput {
				return "", false, false
			}
			jsonOutput = true
		case strings.HasPrefix(arg, "-") || sampleID != "":
			return "", false, false
		default:
			sampleID = arg
		}
	}
	if sampleID == "" {
		return "", false, false
	}
	return sampleID, jsonOutput, true
}

// failLogLines bounds the failure output a verify prints. Enough to see the
// assertion that failed and the line above it; not the whole build.
const failLogLines = 25

// lastLines returns the final n non-empty-trailing lines of s.
func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// originReceipt is the receipt that should enter the cross-verification
// queue as the origin. Prefer the earliest server-acceptable v2 PASS, then
// fall back to a legacy v1 PASS.
//
// It was the LAST receipt stored, whatever it said. Verifying once
// successfully and then again on a machine without Docker — which is an
// ordinary thing to do, and which produces a SKIPPED contract — sent the
// skipped one as the origin. A receipt that proves nothing then occupies
// the origin slot, and the pass that did prove something arrives later
// looking like an independent confirmation of it.
func originReceipt(ctx context.Context, env *sampleEnv, sampleID string) (domain.VerificationReceipt, bool) {
	receipts, err := env.db.ReceiptsForSample(ctx, sampleID)
	if err != nil || len(receipts) == 0 {
		return domain.VerificationReceipt{}, false
	}
	// b904d67 briefly emitted resolvedPackages while still labeling the
	// receipt schemaVersion 1. The v2-aware server must reject that hybrid,
	// and ReceiptsForSample orders it before a later repair verification.
	// Selecting the first PASS forever would therefore make an upgraded
	// client keep reposting a document the upgraded server cannot accept.
	for _, r := range receipts {
		if r.SchemaVersion == 2 && r.Stages["contract"] == sandbox.ResultPass {
			return r, true
		}
	}
	for _, r := range receipts {
		if r.SchemaVersion == 1 && r.ResolvedPackages == nil && r.Stages["contract"] == sandbox.ResultPass {
			return r, true
		}
	}
	// Nothing passed: there is no origin to register, and sending a
	// failure as one would be a claim of its own.
	return domain.VerificationReceipt{}, false
}

// --- publish -----------------------------------------------------------

// publishApprovalForTest is a package-private dependency seam. Production
// builds leave it nil, so no environment variable or command-line flag can
// bypass the typed human approval. Tests in this package may install a
// temporary function without shipping a remotely triggerable bypass.
// sampleContainerOS asks the Docker daemon which kind of container it serves.
// It is a variable so a test can state the answer.
var sampleContainerOS = sandbox.DetectContainerOS

// sampleRunner builds the sandbox this verification will run in.
//
// The container OS has to be asked for, not assumed. StageEnvironment stamps
// the receipt from the runner's ContainerOS, and an unset one reads as linux —
// so a machine serving Windows containers would have produced receipts
// claiming linux, which is the one thing a compatibility network must never
// say. The worker path (worker.go) already asks; this path did not.
func sampleRunner(ctx context.Context, capability domain.SandboxCapability) sandbox.Runner {
	if capability != domain.CapContainerRun {
		return sandbox.NativeRunner{}
	}
	return sandbox.DockerRunner{ContainerOS: sampleContainerOS(ctx)}
}

var publishApprovalForTest func() bool

func samplePublish(ctx context.Context, args []string) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(sampleStderr, "usage: csx sample publish <sampleId> [--seeder name | --anonymous] [--server URL]")
		return 2
	}
	idArg := args[0]
	fs := flag.NewFlagSet("sample publish", flag.ContinueOnError)
	fs.SetOutput(sampleStderr)
	seederFlag := fs.String("seeder", "", "publish under this seeder name")
	anonymous := fs.Bool("anonymous", false, "publish without seeder attribution")
	server := fs.String("server", "", "server URL (default: config serverUrl)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *seederFlag != "" && *anonymous {
		fmt.Fprintln(sampleStderr, "csx sample publish: --seeder and --anonymous are mutually exclusive")
		return 2
	}

	env, err := openSampleEnv()
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: %v\n", err)
		return 1
	}
	defer env.Close()

	row, err := resolveLocalSample(ctx, env.db, idArg)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: %v\n", err)
		return 1
	}
	// Client-side integrity gate: the bytes we would upload must hash to
	// the sampleId we claim. A mismatch aborts BEFORE any upload.
	tgz, err := readSampleArtifact(env.cas, row.SampleID)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: REFUSED: %v\n", err)
		return 1
	}

	dir, err := unpackToTemp(tgz)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)

	// Leakage re-scan at publish time; findings always refuse (no override
	// flag exists in v1 — fix the files and re-create the sample).
	// The provenance names come from where the CONTRIBUTOR is standing,
	// not from the unpacked temp copy: the sample tree cannot know which
	// project it was written inside. Both fields were empty at every call
	// site, so this check compiled no patterns and matched nothing.
	findings, err := samples.Scan(dir, publishProvenance())
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: leakage scan: %v\n", err)
		return 1
	}
	if len(findings) > 0 {
		fmt.Fprintf(sampleStderr, "csx sample publish: REFUSED — %d leakage finding(s):\n", len(findings))
		printFindings(sampleStderr, findings)
		fmt.Fprintln(sampleStderr, "Fix the files and run `csx sample create` again. There is no override flag.")
		// A URL finding is the one that most often surprises an author of a
		// legitimate sample: a sample ABOUT network behaviour has to contain
		// hosts, and "proxy.corp" is indistinguishable from a real internal
		// one. The scanner cannot tell an invented host from a company's,
		// which is exactly why it refuses -- so the message has to say what
		// to write instead rather than only that this was wrong.
		for _, f := range findings {
			if f.Kind == samples.KindAbsolutePath {
				fmt.Fprintln(sampleStderr,
					"\nAn absolute path is refused because its next segment can carry a person,")
				fmt.Fprintln(sampleStderr,
					"a project or an employer, and nothing here can tell whether yours does.")
				fmt.Fprintln(sampleStderr,
					"A sample needs no absolute path: use a relative one, or a path the")
				fmt.Fprintln(sampleStderr,
					"contract creates itself in a temp directory.")
				break
			}
		}
		for _, f := range findings {
			if f.Kind == samples.KindURL {
				fmt.Fprintln(sampleStderr,
					"\nA URL finding does not mean the host is real — it means nothing here can tell.")
				fmt.Fprintln(sampleStderr,
					"For hosts a sample invents, use the names reserved for exactly this:")
				fmt.Fprintln(sampleStderr,
					"  example.com / example.org / example.net, anything under .test, .invalid or .local,")
				fmt.Fprintln(sampleStderr,
					"  localhost, and the documentation IP ranges 192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24.")
				break
			}
		}
		return 1
	}

	var manifest domain.SampleManifest
	if err := json.Unmarshal([]byte(row.ManifestJSON), &manifest); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: parse local manifest: %v\n", err)
		return 1
	}

	// Attribution requires a token: without one the server has nothing to
	// bind a name to and records the sample as anonymous. The approval
	// screen printed the REQUESTED name regardless, so `--seeder acme-labs`
	// while logged out showed "Seeder: acme-labs", the user typed yes to
	// that screen, and the sample published anonymously. A preview whose
	// whole promise is that it shows everything must not show something
	// that will not happen.
	seeder := "anonymous"
	useToken := false
	requested := ""
	switch {
	case *anonymous:
	case *seederFlag != "":
		requested = *seederFlag
		if env.cfg.APIToken != "" {
			seeder, useToken = *seederFlag, true
		}
	case env.cfg.GithubLogin != "":
		requested = env.cfg.GithubLogin
		if env.cfg.APIToken != "" {
			seeder, useToken = env.cfg.GithubLogin, true
		}
	}

	serverURL := strings.TrimRight(*server, "/")
	if serverURL == "" {
		serverURL = strings.TrimRight(env.cfg.ServerURL, "/")
	}

	paths, err := listSampleFiles(dir)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: %v\n", err)
		return 1
	}
	fmt.Fprintf(sampleStdout, "About to publish %s to %s\n", row.SampleID, serverURL)
	fmt.Fprintf(sampleStdout, "License: %s\n", manifest.License)
	fmt.Fprintf(sampleStdout, "Seeder:  %s\n", seeder)
	if requested != "" && !useToken {
		fmt.Fprintf(sampleStdout,
			"         (%q needs a signed-in account; run `csx login` first, or this publishes anonymously)\n",
			requested)
	}
	fmt.Fprintf(sampleStdout, "Files (%d):\n", len(paths))
	for _, p := range paths {
		fmt.Fprintf(sampleStdout, "  %s\n", p)
	}
	fmt.Fprintln(sampleStdout, "Run `csx sample preview` to inspect every file's content before approving.")

	if publishApprovalForTest == nil || !publishApprovalForTest() {
		fmt.Fprint(sampleStdout, "[PUBLISH] Type \"yes\" to publish this sample publicly: ")
		line, rerr := bufio.NewReader(sampleStdin).ReadString('\n')
		if rerr != nil && line == "" {
			fmt.Fprintln(sampleStderr, "csx sample publish: aborted (no approval)")
			return 1
		}
		if strings.TrimSpace(line) != "yes" {
			fmt.Fprintln(sampleStderr, "csx sample publish: aborted — approval requires typing exactly \"yes\"")
			return 1
		}
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("manifest", row.ManifestJSON); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: %v\n", err)
		return 1
	}
	if err := mw.WriteField("sampleId", row.SampleID); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: %v\n", err)
		return 1
	}
	fw, err := mw.CreateFormFile("artifact", "sample.tar.gz")
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: %v\n", err)
		return 1
	}
	if _, err := fw.Write(tgz); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: %v\n", err)
		return 1
	}
	if err := mw.Close(); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: %v\n", err)
		return 1
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/v1/samples", &body)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if useToken {
		req.Header.Set("Authorization", "Bearer "+env.cfg.APIToken)
	}
	resp, err := sampleHTTP.Do(req)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: upload: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(sampleStderr, "csx sample publish: server rejected upload: HTTP %d\n%s\n",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
		return 1
	}

	if err := env.db.SetSampleStatus(ctx, row.SampleID, "PUBLISHED"); err != nil {
		fmt.Fprintf(sampleStderr, "csx sample publish: update local status: %v\n", err)
		return 1
	}

	// Origin PASS enters the cross-verification queue as the first receipt
	// (goal.md §9.6); without it a later peer receipt would wrongly become
	// the origin. Best-effort: a failed post leaves the receipt local.
	if origin, ok := originReceipt(ctx, env, row.SampleID); ok {
		if b, merr := json.Marshal(origin); merr == nil {
			vreq, verr := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/v1/verifications", bytes.NewReader(b))
			if verr == nil {
				vreq.Header.Set("Content-Type", "application/json")
				if vresp, derr := sampleHTTP.Do(vreq); derr == nil {
					io.Copy(io.Discard, io.LimitReader(vresp.Body, 4096))
					vresp.Body.Close()
					if vresp.StatusCode >= 200 && vresp.StatusCode < 300 {
						fmt.Fprintln(sampleStdout, "Origin verification receipt registered.")
					} else {
						fmt.Fprintf(sampleStderr, "warning: origin receipt not accepted (HTTP %d); run `csx sample verify` then republish it\n", vresp.StatusCode)
					}
				}
			}
		}
	} else {
		fmt.Fprintln(sampleStdout, "Note: no local verification receipt — run `csx sample verify` first so your origin PASS enters the cross-verification queue.")
	}
	if cur, _, gerr := env.db.GetStat(ctx, "originSeeds"); gerr == nil {
		n, _ := strconv.Atoi(cur)
		_ = env.db.SetStat(ctx, "originSeeds", strconv.Itoa(n+1))
	}
	fmt.Fprintf(sampleStdout, "Published. Public URL: %s/samples/%s\n", serverURL, row.SampleID)
	return 0
}

// --- remove ------------------------------------------------------------

func sampleRemove(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(sampleStderr, "usage: csx sample remove <fullSampleId>")
		return 2
	}
	sampleID := args[0]
	if !exactSampleID(sampleID) {
		fmt.Fprintln(sampleStderr, "csx sample remove: sample id must be exactly sha256: followed by 64 lowercase hexadecimal characters")
		return 2
	}

	env, err := openSampleEnv()
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample remove: %v\n", err)
		return 1
	}
	defer env.Close()

	row, ok, err := env.db.GetSample(ctx, sampleID)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample remove: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(sampleStderr, "csx sample remove: local sample %s not found\n", sampleID)
		return 1
	}

	fmt.Fprintf(sampleStdout, "Sample: %s\nStatus: %s\nCase:   %s\n", row.SampleID, row.Status, row.CaseID)
	fmt.Fprintln(sampleStdout, "This removes the local sample, its verification receipts, search entry, and cached artifact.")
	fmt.Fprint(sampleStdout, "Type the full sample id to confirm: ")
	line, readErr := bufio.NewReader(sampleStdin).ReadString('\n')
	if readErr != nil && readErr != io.EOF {
		fmt.Fprintf(sampleStderr, "csx sample remove: read confirmation: %v\n", readErr)
		return 1
	}
	if strings.TrimSpace(line) != sampleID {
		fmt.Fprintln(sampleStderr, "csx sample remove: aborted — confirmation did not exactly match the full sample id")
		return 1
	}

	removed, err := env.db.RemoveSample(ctx, sampleID)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample remove: remove local evidence: %v\n", err)
		return 1
	}
	if !removed {
		fmt.Fprintf(sampleStderr, "csx sample remove: local sample %s disappeared before removal\n", sampleID)
		return 1
	}
	if err := env.cas.Delete(sampleID); err != nil && !os.IsNotExist(err) {
		// The searchable evidence is already gone. An unreferenced CAS object
		// is inert and can be retried safely; do not pretend filesystem cleanup
		// was complete when the exact-object deletion failed.
		fmt.Fprintf(sampleStderr, "csx sample remove: local evidence removed, but cached artifact cleanup failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(sampleStdout, "Removed local sample %s. Shared case and unrelated CAS objects were kept.\n", sampleID)
	return 0
}

// --- list --------------------------------------------------------------

func sampleList(ctx context.Context, args []string) int {
	env, err := openSampleEnv()
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample list: %v\n", err)
		return 1
	}
	defer env.Close()

	rows, err := env.db.ListSamples(ctx)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample list: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(sampleStdout, "No local samples. Start with: csx sample propose --goal \"...\"")
		return 0
	}
	fmt.Fprintf(sampleStdout, "%-14s %-11s %-8s %-6s %s\n", "SAMPLE", "STATUS", "LICENSE", "PINNED", "GOAL")
	for _, r := range rows {
		var manifest domain.SampleManifest
		_ = json.Unmarshal([]byte(r.ManifestJSON), &manifest)
		short := strings.TrimPrefix(r.SampleID, "sha256:")
		if len(short) > 12 {
			short = short[:12]
		}
		pinned := "no"
		if r.Pinned {
			pinned = "yes"
		}
		fmt.Fprintf(sampleStdout, "%-14s %-11s %-8s %-6s %s\n",
			short, r.Status, r.License, pinned, manifest.Case.Goal)
	}
	return 0
}

// samplePending lists prepared-but-unreviewed sample workspaces.
//
// Publishing needs the user's explicit approval (goal.md §12.4), which
// means an agent can prepare a sample but cannot finish the job. Before
// this, that half-finished state was invisible: the workspace was created,
// filled in, and forgotten. Every proposal nobody reviews is a sample the
// network never gets.
func samplePending(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("sample pending", flag.ContinueOnError)
	fs.SetOutput(sampleStderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	env, err := openSampleEnv()
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample pending: %v\n", err)
		return 1
	}
	defer env.Close()

	rows, err := env.db.PendingProposals(ctx)
	if err != nil {
		fmt.Fprintf(sampleStderr, "csx sample pending: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(sampleStdout, "Nothing pending. Your agent creates these with propose_public_sample")
		fmt.Fprintln(sampleStdout, "after it solves something worth sharing.")
		return 0
	}

	fmt.Fprintf(sampleStdout, "%d sample(s) prepared and waiting for your review:\n\n", len(rows))
	for _, r := range rows {
		fmt.Fprintf(sampleStdout, "  %s\n", r.Goal)
		if len(r.Packages) > 0 {
			fmt.Fprintf(sampleStdout, "    packages: %s\n", strings.Join(r.Packages, ", "))
		}
		fmt.Fprintf(sampleStdout, "    prepared: %s\n", r.CreatedAt.Local().Format("2006-01-02 15:04"))
		fmt.Fprintf(sampleStdout, "    review:   csx sample create %s\n\n", r.Workdir)
	}
	fmt.Fprintln(sampleStdout, "`create` prints a sample id; `csx sample preview <id>` then shows every")
	fmt.Fprintln(sampleStdout, "file that would be published, and nothing leaves your machine until you")
	fmt.Fprintln(sampleStdout, "type the confirmation in `csx sample publish <id>`.")
	return 0
}

// publishProvenance derives the project-identifying names for the publish
// gate. The sample has been unpacked to a temp directory by then, so the
// names have to come from where the user is running the command — which is
// their project, in every real case.
func publishProvenance() samples.ScanOptions {
	cwd, err := os.Getwd()
	if err != nil {
		return samples.ScanOptions{}
	}
	return samples.ProvenanceOptions(cwd)
}
