package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/search"
	"github.com/r2cuerdame/codesamplex/internal/verifier"
)

// The fix worker (#444). `csx fix-claims work` is the loop a Farm lane
// runs: take a lease, find the reproducer for it, execute the same sample
// at every release and environment the planner asked for, file each
// receipt, and hand the runs back. `csx fix-claims probe` is one step of
// that by hand, for a reproducer author checking a single release.
//
// A probe never edits the reproducer's code or contract. It copies the
// directory, repins the candidate's package (fixclaims.Repin), regenerates
// the lockfile with the ecosystem's own tool, creates the sample, verifies
// it in the local sandbox, submits the sample as a private draft and posts
// the signed receipt. What comes back is a run the server can check
// against that receipt -- the worker asserts nothing the receipt does not.

// fixReproducerFile is the document a reproducer directory carries beside
// its csx.json: the candidate it answers, where the reproducer came from,
// and any environments the author asks for beyond the planner's.
const fixReproducerFile = "fix-claim.json"

// FixReproducer is the parsed fix-claim.json.
type fixReproducer struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Source        fixclaims.ReproducerSource `json:"source"`
	Candidate     fixclaims.Candidate        `json:"candidate"`
	// Environments are pairs the author wants beyond the planner's, as
	// bounded coordinates (os, runtime, runtimeVersion). The upstream
	// issue often names the runtime the bug is tied to when the release
	// line does not; this is where that reading goes.
	Environments []fixclaims.Environment `json:"environments,omitempty"`
	// Notes is for the reader; nothing parses it.
	Notes string `json:"notes,omitempty"`
	dir   string
}

// fixProbeResult is what one probe execution yields: the run to file and
// a one-line detail for the log.
type fixProbeResult struct {
	Run    fixclaims.Run
	Detail string
}

// fixClaimsExecute runs one probe end to end; tests replace it so the
// work loop can be exercised without Docker or a registry.
var fixClaimsExecute = executeFixProbe

// fixClaimsProbeCapability lets tests force COMPILE_ONLY without a
// docker binary on PATH.
var fixClaimsProbeCapability = func(ctx context.Context) domain.SandboxCapability {
	if verifierCapability != "" {
		return verifierCapability
	}
	return sandbox.Detect(ctx)
}

// loadFixReproducers reads every fix-claim.json one level below root.
func loadFixReproducers(root string) ([]fixReproducer, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []fixReproducer
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, fixReproducerFile))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var r fixReproducer
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, fixReproducerFile), err)
		}
		if !fixclaims.ValidReproducerSource(r.Source) {
			return nil, fmt.Errorf("%s: source must be EXISTING_SAMPLE, UPSTREAM_REPRO or GENERATED", filepath.Join(dir, fixReproducerFile))
		}
		if _, err := os.Stat(filepath.Join(dir, "csx.json")); err != nil {
			return nil, fmt.Errorf("%s: no csx.json beside %s", dir, fixReproducerFile)
		}
		r.dir = dir
		out = append(out, r)
	}
	return out, nil
}

// matchFixReproducer finds the reproducer written for a candidate by the
// candidate's identity (package, claimed-fixed release, normalized claim).
func matchFixReproducer(repros []fixReproducer, c fixclaims.Candidate) (fixReproducer, bool) {
	key := c.DedupKey()
	for _, r := range repros {
		if r.Candidate.DedupKey() == key {
			return r, true
		}
	}
	return fixReproducer{}, false
}

func fixClaimsWork(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fix-claims work", flag.ContinueOnError)
	fs.SetOutput(fixClaimsStderr)
	server, token := fixClaimsServerFlags(fs)
	root := fs.String("repro-root", "", "directory whose subdirectories hold reproducers, each with fix-claim.json and csx.json")
	osFlag := fs.String("os", "", "comma-separated operating systems this worker can run (default: the verifier's own)")
	once := fs.Bool("once", false, "take one lease and stop")
	maxLeases := fs.Int("max", 0, "stop after N leases (0 = until the queue is empty)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		fixClaimsUsage()
		return 2
	}
	base, tok, ok := fixClaimsResolve(*server, *token)
	if !ok {
		return 2
	}
	repros, err := loadFixReproducers(*root)
	if err != nil {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims work: %v\n", err)
		return 2
	}
	envelope := map[string]any{"schemaVersion": 1}
	if *osFlag != "" {
		var oses []string
		for _, o := range strings.Split(*osFlag, ",") {
			if o = strings.TrimSpace(strings.ToLower(o)); o != "" {
				oses = append(oses, o)
			}
		}
		envelope["verifierOS"] = oses
	} else if verifierOS := sampleWorkerEnvelope(ctx)["verifierOS"]; verifierOS != nil {
		envelope["verifierOS"] = verifierOS
	}
	poll, _ := json.Marshal(envelope)

	leases := 0
	for {
		if ctx.Err() != nil {
			return 1
		}
		code, body, ok := fixClaimsCall(ctx, http.MethodPost, base, tok, "/v1/fix-claims/work/next", poll)
		if !ok || code != http.StatusOK {
			fmt.Fprintf(fixClaimsStderr, "csx fix-claims work: server rejected the poll (HTTP %d): %v\n", code, body["error"])
			return 1
		}
		status, _ := body["status"].(string)
		if status != "ASSIGNED" {
			fmt.Fprintf(fixClaimsStdout, "%s after %d lease(s)\n", status, leases)
			return 0
		}
		leases++
		work, _ := body["work"].(map[string]any)
		rawWork, _ := json.Marshal(work)
		var lease struct {
			ID        int64               `json:"id"`
			Candidate fixclaims.Candidate `json:"candidate"`
			Probes    []fixclaims.Probe   `json:"probes"`
			Runs      []fixclaims.Run     `json:"runs"`
		}
		if err := json.Unmarshal(rawWork, &lease); err != nil || lease.ID <= 0 || len(lease.Probes) == 0 {
			fmt.Fprintln(fixClaimsStderr, "csx fix-claims work: invalid work document")
			return 1
		}
		fmt.Fprintf(fixClaimsStdout, "lease %d: %s %s -> %s: %s\n", lease.ID, lease.Candidate.Package().String(),
			lease.Candidate.ClaimedBadVersion, lease.Candidate.ClaimedFixedVersion, lease.Candidate.Claim)
		repro, found := matchFixReproducer(repros, lease.Candidate)
		if !found {
			fixClaimsOutcome(ctx, base, tok, lease.ID, "NO_REPRODUCER", "no reproducer directory for this candidate")
			fmt.Fprintf(fixClaimsStdout, "  no reproducer; handed back\n")
			if *once || (*maxLeases > 0 && leases >= *maxLeases) {
				return 0
			}
			continue
		}
		if repro.Source != fixclaims.ReproducerExistingSample {
			reproPayload, _ := json.Marshal(map[string]any{"schemaVersion": 1, "source": repro.Source})
			_, _, _ = fixClaimsCall(ctx, http.MethodPost, base, tok, fmt.Sprintf("/v1/fix-claims/%d/reproducer", lease.ID), reproPayload)
		}
		probes := fixProbesWithAuthorEnvironments(lease.Candidate, lease.Probes, lease.Runs, repro.Environments)
		var runs []fixclaims.Run
		var failures []string
		for _, probe := range probes {
			fmt.Fprintf(fixClaimsStdout, "  probe %s @ %s (%s)\n", probe.Version, probe.Environment.Key(), probe.Reason)
			res, err := fixClaimsExecute(ctx, base, tok, lease.Candidate, repro.dir, probe)
			if err != nil {
				fmt.Fprintf(fixClaimsStdout, "    infrastructure: %v\n", err)
				failures = append(failures, err.Error())
				continue
			}
			fmt.Fprintf(fixClaimsStdout, "    %s %s\n", res.Run.Verdict, res.Detail)
			runs = append(runs, res.Run)
		}
		if len(runs) == 0 {
			fixClaimsOutcome(ctx, base, tok, lease.ID, "INFRASTRUCTURE", strings.Join(failures, "; "))
		} else {
			doc, _ := json.Marshal(map[string]any{"schemaVersion": 1, "runs": runs})
			code, body, ok := fixClaimsCall(ctx, http.MethodPost, base, tok, fmt.Sprintf("/v1/fix-claims/%d/runs", lease.ID), doc)
			if !ok || code != http.StatusOK {
				fmt.Fprintf(fixClaimsStderr, "csx fix-claims work: server refused the runs (HTTP %d): %v\n", code, body["error"])
				fixClaimsOutcome(ctx, base, tok, lease.ID, "INFRASTRUCTURE", fmt.Sprintf("runs refused: %v", body["error"]))
				return 1
			}
			record, _ := body["record"].(map[string]any)
			fmt.Fprintf(fixClaimsStdout, "  recorded: status=%v pairOutcome=%v\n", record["status"], record["pairOutcome"])
		}
		if *once || (*maxLeases > 0 && leases >= *maxLeases) {
			return 0
		}
	}
}

// fixProbesWithAuthorEnvironments appends the pair in each environment the
// reproducer's author asked for and the planner did not, skipping any
// version+environment that already has a run.
func fixProbesWithAuthorEnvironments(c fixclaims.Candidate, planned []fixclaims.Probe, runs []fixclaims.Run, extra []fixclaims.Environment) []fixclaims.Probe {
	seen := map[string]bool{}
	for _, r := range runs {
		seen[r.Environment.Key()+"@"+r.Version] = true
	}
	for _, p := range planned {
		seen[p.Environment.Key()+"@"+p.Version] = true
	}
	out := append([]fixclaims.Probe(nil), planned...)
	c = c.Normalized()
	for _, env := range extra {
		env.OS = strings.ToLower(strings.TrimSpace(env.OS))
		if env.OS == "" {
			env.OS = fixclaims.DefaultEnvironment.OS
		}
		for _, v := range []string{c.ClaimedBadVersion, c.ClaimedFixedVersion} {
			if v == "" || seen[env.Key()+"@"+v] {
				continue
			}
			seen[env.Key()+"@"+v] = true
			out = append(out, fixclaims.Probe{Version: v, Environment: env, Reason: fixclaims.ProbeEnvironmentHint})
		}
	}
	return out
}

func fixClaimsOutcome(ctx context.Context, base, tok string, id int64, outcome, detail string) {
	if len(detail) > 400 {
		detail = detail[:400]
	}
	payload, _ := json.Marshal(map[string]any{"schemaVersion": 1, "outcome": outcome, "detail": detail})
	code, body, ok := fixClaimsCall(ctx, http.MethodPost, base, tok, fmt.Sprintf("/v1/fix-claims/%d/outcome", id), payload)
	if !ok || code != http.StatusOK {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims work: outcome %s refused (HTTP %d): %v\n", outcome, code, body["error"])
	}
}

func fixClaimsProbe(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fix-claims probe", flag.ContinueOnError)
	fs.SetOutput(fixClaimsStderr)
	server, token := fixClaimsServerFlags(fs)
	dir := fs.String("repro", "", "reproducer directory holding fix-claim.json and csx.json")
	version := fs.String("version", "", "release of the candidate's package to run")
	osFlag := fs.String("os", "linux", "environment os to file the run under")
	runtime := fs.String("runtime", "", "environment runtime to file the run under (optional)")
	runtimeVersion := fs.String("runtime-version", "", "runtime version to declare in the manifest and file the run under (optional)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" || *version == "" {
		fixClaimsUsage()
		return 2
	}
	base, tok, ok := fixClaimsResolve(*server, *token)
	if !ok {
		return 2
	}
	raw, err := os.ReadFile(filepath.Join(*dir, fixReproducerFile))
	if err != nil {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims probe: %v\n", err)
		return 2
	}
	var repro fixReproducer
	if err := json.Unmarshal(raw, &repro); err != nil {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims probe: %s: %v\n", fixReproducerFile, err)
		return 2
	}
	probe := fixclaims.Probe{
		Version:     *version,
		Environment: fixclaims.Environment{OS: strings.ToLower(*osFlag), Runtime: strings.ToLower(*runtime), RuntimeVersion: strings.ToLower(*runtimeVersion)},
		Reason:      "manual",
	}
	res, err := fixClaimsExecute(ctx, base, tok, repro.Candidate, *dir, probe)
	if err != nil {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims probe: %v\n", err)
		return 1
	}
	fixClaimsPrintJSON(map[string]any{"run": res.Run, "detail": res.Detail})
	return 0
}

// executeFixProbe is the real probe: copy, repin, relock, create, verify,
// submit the draft, post the receipt.
func executeFixProbe(ctx context.Context, base, tok string, c fixclaims.Candidate, reproDir string, probe fixclaims.Probe) (fixProbeResult, error) {
	started := time.Now()
	work, err := os.MkdirTemp("", "csx-fix-probe-*")
	if err != nil {
		return fixProbeResult{}, err
	}
	defer os.RemoveAll(work)
	if err := copyReproducer(reproDir, work); err != nil {
		return fixProbeResult{}, err
	}
	if err := fixclaims.Repin(work, c, probe.Version, probe.Environment); err != nil {
		return fixProbeResult{}, err
	}
	c = c.Normalized()
	if err := fixclaims.Relock(ctx, work, c.Ecosystem); err != nil {
		// A release the registry does not have, or one whose dependency
		// tree cannot be resolved, is not a verdict on the bug: the
		// version is unrunnable and the run says so.
		return fixProbeResult{
			Run: fixclaims.Run{Version: probe.Version, Environment: probe.Environment, Verdict: fixclaims.VerdictUnrunnable,
				FarmSeconds: int64(time.Since(started).Seconds()), ObservedAt: time.Now().UTC()},
			Detail: "lock: " + err.Error(),
		}, nil
	}
	rawManifest, err := os.ReadFile(filepath.Join(work, "csx.json"))
	if err != nil {
		return fixProbeResult{}, err
	}
	var manifest domain.SampleManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		return fixProbeResult{}, fmt.Errorf("csx.json: %w", err)
	}
	if manifest.License == "" {
		manifest.License = samples.DefaultLicense
	}
	created, err := samples.CreateFromDir(ctx, work, manifest)
	if err != nil {
		return fixProbeResult{}, err
	}
	if len(created.Findings) > 0 {
		return fixProbeResult{}, fmt.Errorf("reproducer has %d leakage finding(s); fix them before it can be submitted", len(created.Findings))
	}
	env, err := openSampleEnv()
	if err != nil {
		return fixProbeResult{}, err
	}
	defer env.Close()
	if _, err := env.cas.Put(bytes.NewReader(created.Artifact)); err != nil {
		return fixProbeResult{}, err
	}
	if err := search.SeedSampleDoc(ctx, env.db, created.Manifest, created.SampleID, "LOCAL"); err != nil {
		return fixProbeResult{}, err
	}
	row, ok, err := env.db.GetSample(ctx, created.SampleID)
	if err != nil || !ok {
		return fixProbeResult{}, fmt.Errorf("reload sample row: %v", err)
	}
	row.HasArtifact = true
	if err := env.db.SaveSample(ctx, row); err != nil {
		return fixProbeResult{}, err
	}

	// The server must hold the sample before it can hold a receipt for
	// it. It goes up as a private draft, exactly as a sample worker's
	// would; a FAIL receipt keeps it a draft, which is what it is.
	if err := postAuthoringDraft(ctx, base, tok, row.ManifestJSON, created.SampleID, "LOCAL", created.Artifact); err != nil {
		return fixProbeResult{}, err
	}

	capability := fixClaimsProbeCapability(ctx)
	if capability != domain.CapContainerRun {
		// Without a container nothing in the sample executes, so there is
		// no verdict to file. This is not UNRUNNABLE -- the package may be
		// fine -- it is this machine being unable to judge.
		return fixProbeResult{}, errors.New("no container isolation on this host; a fix probe needs CONTAINER_RUN")
	}
	runner := verifierRunner
	if runner == nil {
		runner = sampleRunner(ctx, capability)
	}
	ident, err := identity.LoadOrCreate(env.home)
	if err != nil {
		return fixProbeResult{}, err
	}
	dir, err := unpackToTemp(created.Artifact)
	if err != nil {
		return fixProbeResult{}, err
	}
	defer os.RemoveAll(dir)
	fp := environment.Collect(ctx, map[string]string{"ecosystem": created.Manifest.Environment.Ecosystem})
	receipt, stageLogs, err := verifier.RunLogged(ctx, runner, capability, dir, created.Manifest, ident, fp)
	if err != nil {
		return fixProbeResult{}, err
	}
	if receipt.SampleID != created.SampleID {
		return fixProbeResult{}, fmt.Errorf("rebuilt sample id %s != %s", receipt.SampleID, created.SampleID)
	}
	if err := env.db.SaveReceipt(ctx, receipt); err != nil {
		return fixProbeResult{}, err
	}
	run, detail := fixRunFromReceipt(receipt, stageLogs, probe)
	run.FarmSeconds = int64(time.Since(started).Seconds())
	if run.Verdict == fixclaims.VerdictUnrunnable {
		return fixProbeResult{Run: run, Detail: detail}, nil
	}
	if err := postReceipt(ctx, base, tok, receipt); err != nil {
		return fixProbeResult{}, err
	}
	return fixProbeResult{Run: run, Detail: detail}, nil
}

// fixRunFromReceipt reads the verdict off a receipt. Only the contract
// stage decides: a contract PASS is PASS, a contract FAIL is FAIL with the
// receipt's own failure fingerprint. A resolve or compile failure means
// the contract never ran, and that is UNRUNNABLE -- a build that breaks on
// one release is a fact worth the detail line, but it is not the bug the
// contract asserts, and the server admits no verdict without a contract
// result to match it against.
func fixRunFromReceipt(receipt domain.VerificationReceipt, stageLogs map[string]string, probe fixclaims.Probe) (fixclaims.Run, string) {
	run := fixclaims.Run{
		Version:     probe.Version,
		Environment: probe.Environment,
		ReceiptID:   receipt.ReceiptID(),
		SampleID:    receipt.SampleID,
		ObservedAt:  time.Now().UTC(),
	}
	switch receipt.Stages["contract"] {
	case sandbox.ResultPass:
		run.Verdict = fixclaims.VerdictPass
		return run, "contract passed"
	case sandbox.ResultFail:
		run.Verdict = fixclaims.VerdictFail
		if f, ok := receipt.StageFailures["contract"]; ok {
			run.FailureFingerprint = f.Fingerprint
			return run, "contract failed: " + f.ErrorSummary
		}
		return run, "contract failed"
	}
	run.Verdict = fixclaims.VerdictUnrunnable
	run.ReceiptID, run.SampleID = "", ""
	for _, stage := range []string{"resolve", "compile"} {
		if receipt.Stages[stage] != sandbox.ResultFail {
			continue
		}
		detail := stage + " failed"
		if f, ok := receipt.StageFailures[stage]; ok && f.ErrorSummary != "" {
			detail += ": " + f.ErrorSummary
		} else if tail := lastLines(stageLogs[stage], 3); tail != "" {
			detail += ": " + strings.ReplaceAll(tail, "\n", " | ")
		}
		return run, detail
	}
	return run, "contract did not run"
}

// copyReproducer copies a reproducer tree, leaving behind what a previous
// local run may have generated. The verifier's own artifact builder
// excludes the same directories.
func copyReproducer(src, dst string) error {
	skip := map[string]bool{"node_modules": true, "target": true, ".csx-vendor": true, "__pycache__": true, ".git": true, ".venv": true}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
}

// postAuthoringDraft uploads a local sample as a private draft under the
// session token: the same request csx sample-worker submit makes.
func postAuthoringDraft(ctx context.Context, base, tok, manifestJSON, sampleID, localStatus string, artifact []byte) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, value := range map[string]string{"manifest": manifestJSON, "sampleId": sampleID, "localStatus": localStatus} {
		if err := mw.WriteField(name, value); err != nil {
			return err
		}
	}
	fw, err := mw.CreateFormFile("artifact", "sample.tar.gz")
	if err != nil {
		return err
	}
	if _, err := fw.Write(artifact); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/authoring/drafts", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	resp, err := fixClaimsClient.Do(req)
	if err != nil {
		return fmt.Errorf("draft upload failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, sampleWorkerResponseLimit))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server rejected the draft (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var result struct {
		SampleID string `json:"sampleId"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.SampleID != sampleID {
		return errors.New("invalid draft response")
	}
	return nil
}

// postReceipt files a signed receipt with the server.
func postReceipt(ctx context.Context, base, tok string, receipt domain.VerificationReceipt) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/verifications", bytes.NewReader(domain.MustCanonicalJSON(receipt)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := fixClaimsClient.Do(req)
	if err != nil {
		return fmt.Errorf("receipt upload failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, sampleWorkerResponseLimit))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server rejected the receipt (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}
