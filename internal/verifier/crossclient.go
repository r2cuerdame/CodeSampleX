package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

// idleWindow: idle-budget verification only proceeds when no csx run
// activity happened within this window (plan P8.3).
const idleWindow = 10 * time.Minute

const (
	CrossJobReason  = "cross"
	MatrixJobReason = "matrix"
)

// Job is one open cross/matrix verification job (plan C5).
type Job struct {
	ID       int64           `json:"id"`
	SampleID string          `json:"sampleId"`
	Reason   string          `json:"reason"` // "cross" | "matrix"
	WantEnv  json.RawMessage `json:"wantEnv,omitempty"`
}

// CrossVerifier claims verification jobs from the central server,
// re-verifies the artifacts in the local sandbox, and posts signed
// receipts back. It uses the persistent peer identity (goal.md §8.6) —
// verification is explicit-identity work, unlike anonymous evidence.
type CrossVerifier struct {
	HTTP      *http.Client
	ServerURL string
	Ident     *identity.Identity
	Cap       domain.SandboxCapability

	// Runner overrides the capability-derived sandbox runner (tests, or a
	// daemon that already detected capability). Nil picks Docker for
	// CONTAINER_RUN and the native COMPILE_ONLY runner otherwise.
	Runner sandbox.Runner
	// ContainerOS is the kind of container this machine's Docker daemon
	// serves ("linux" or "windows"; empty means linux). It decides both
	// which jobs are claimable and which OS the receipt reports, so a
	// Windows worker contributes Windows evidence instead of quietly
	// producing Linux results on a Windows machine.
	ContainerOS string
	// Env is this peer's environment fingerprint stamped into receipts.
	Env domain.EnvironmentFingerprint
	// WorkDir hosts per-job temp workspaces; empty means the OS temp dir.
	WorkDir string
	// LastActivityFile is touched by csx run; its mtime drives the
	// "idle" budget (only verify when the user is not actively working).
	LastActivityFile string
	// StageLogs keeps the output of a verification that failed, on this
	// machine only. Nil disables it entirely, which is what tests and any
	// caller without a home directory want.
	StageLogs *StageLogStore
	// OnStageLogs is called with the path of a kept log so the worker can
	// print it. Without it a failed job left no trace an operator could
	// follow: stdout carried a "job completed" counter and nothing else.
	OnStageLogs func(path string)
	// OnVerified is called after each job whose receipt was accepted.
	//
	// Nothing counted them. The dashboard reads a crossVerifications
	// counter that no code path ever wrote, so a peer could spend hours
	// verifying other people's samples and its own stats would report zero
	// forever -- the one number that shows a peer giving back rather than
	// taking. Optional: nil simply counts nothing.
	OnVerified func()
	// Source resolves artifacts from the cheapest place first (local CAS,
	// then announced peers, then the server) — goal.md §15.1. *peer.Node
	// implements it. Nil downloads straight from the server.
	Source ArtifactSource

	// fetchMu keeps a worker's parallel execution lanes from all listing the
	// same open job before one of them claims it. Verification remains fully
	// parallel; only the tiny list+claim transaction is serialized locally.
	fetchMu sync.Mutex

	// unsupported is what the last scan was offered and had no lane for.
	//
	// Skipping such a row is correct -- claiming work this build cannot run
	// would burn the sample's bounded attempts on a machine that was never
	// going to measure it. What was missing is that the skip left no trace:
	// an empty queue and a queue of impossible work both ended as
	// "completed=0 failed=0", and production stayed in the second state for
	// three days with nobody able to tell the difference.
	unsupportedMu sync.Mutex
	unsupported   []string
}

// ArtifactSource is the peer node's fetch chain, kept as an interface so
// the verifier does not depend on the peer package (the daemon wires them).
type ArtifactSource interface {
	// Fetch returns the artifact bytes and the source they came from
	// ("local" | "peer" | "server").
	Fetch(ctx context.Context, sampleID string) ([]byte, string, error)
}

func (cv *CrossVerifier) client() *http.Client {
	if cv.HTTP != nil {
		return cv.HTTP
	}
	return http.DefaultClient
}

func (cv *CrossVerifier) base() string { return strings.TrimRight(cv.ServerURL, "/") }

func (cv *CrossVerifier) runner() sandbox.Runner {
	if cv.Runner != nil {
		return cv.Runner
	}
	if cv.Cap == domain.CapContainerRun {
		return sandbox.DockerRunner{ContainerOS: cv.ContainerOS}
	}
	return sandbox.NativeRunner{}
}

// FetchJob asks the server for one open job matching this peer's
// capability and claims it via POST /v1/verification/jobs/{id}/claim.
// (nil, nil) means no work is available.
func (cv *CrossVerifier) FetchJob(ctx context.Context) (*Job, error) {
	if cv.Ident == nil {
		return nil, errors.New("verifier: nil identity")
	}
	cv.fetchMu.Lock()
	defer cv.fetchMu.Unlock()

	q := url.Values{}
	q.Set("peerId", cv.Ident.PeerID())
	q.Set("capability", string(cv.Cap))
	// Ask for a small window and choose locally. Capability is filtered by the
	// server; ecosystem/runtime/SDK requirements are checked by this binary.
	// Looking at one row let an incompatible head job hide compatible work.
	q.Set("limit", "20")
	// Say which kind of container this daemon serves, so the window is not
	// filled with rows this machine cannot run. Without it a Linux verifier
	// was handed the Windows jobs too, and the one Windows verifier on the
	// network waited behind them.
	if cv.ContainerOS != "" {
		q.Set("containerOs", cv.ContainerOS)
	}
	u := cv.base() + "/v1/verification/jobs?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cv.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("verifier: list jobs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("verifier: list jobs: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("verifier: list jobs: %w", err)
	}
	var noLane []string
	defer func() { cv.recordUnsupported(noLane) }()
	for i := range body.Jobs {
		job := body.Jobs[i]
		if job.Reason != CrossJobReason && job.Reason != MatrixJobReason {
			continue
		}
		if !cv.canPrepareJob(job) {
			if coordinate, ok := unrunnableCoordinate(job); ok {
				noLane = append(noLane, coordinate)
			}
			continue
		}

		claim, _ := json.Marshal(map[string]string{"peerId": cv.Ident.PeerID()})
		cu := fmt.Sprintf("%s/v1/verification/jobs/%d/claim", cv.base(), job.ID)
		creq, err := http.NewRequestWithContext(ctx, http.MethodPost, cu, bytes.NewReader(claim))
		if err != nil {
			return nil, err
		}
		creq.Header.Set("Content-Type", "application/json")
		cresp, err := cv.client().Do(creq)
		if err != nil {
			return nil, fmt.Errorf("verifier: claim job %d: %w", job.ID, err)
		}
		cresp.Body.Close()
		if cresp.StatusCode < 200 || cresp.StatusCode >= 300 {
			return nil, fmt.Errorf("verifier: claim job %d: HTTP %d", job.ID, cresp.StatusCode)
		}
		return &job, nil
	}
	return nil, nil
}

// UnsupportedWork reports the coordinates the last queue scan offered that no
// verifier image in this build can run, most useful when nothing was claimed.
// An empty result means the queue itself had nothing for this peer, which is
// a different fact and deserves a different sentence.
func (cv *CrossVerifier) UnsupportedWork() []string {
	cv.unsupportedMu.Lock()
	defer cv.unsupportedMu.Unlock()
	return append([]string(nil), cv.unsupported...)
}

func (cv *CrossVerifier) recordUnsupported(coordinates []string) {
	cv.unsupportedMu.Lock()
	defer cv.unsupportedMu.Unlock()
	cv.unsupported = coordinates
}

// unrunnableCoordinate names the requirement a skipped job asked for, in the
// vocabulary an operator can act on: an ecosystem, a runtime and a line. It
// reports false for a job skipped for any other reason -- a malformed
// requirement or a matrix shape this worker does not take -- so the message
// never claims a missing image that is not the reason.
func unrunnableCoordinate(job Job) (string, bool) {
	want, err := parseWorkerRequirements(job.WantEnv)
	if err != nil {
		return "", false
	}
	if want.SandboxCapability != "" && want.SandboxCapability != domain.CapContainerRun {
		return "", false
	}
	parts := make([]string, 0, 4)
	for _, field := range []string{want.Ecosystem, want.Runtime, want.RuntimeVersion} {
		if field != "" {
			parts = append(parts, field)
		}
	}
	if want.ExecutionContext != "" && want.ExecutionContext != want.Runtime {
		parts = append(parts, want.ExecutionContext)
	}
	if want.BrowserFamily != "" {
		parts = append(parts, want.BrowserFamily+" "+want.BrowserMajor)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, " "), true
}

func parseWorkerRequirements(raw json.RawMessage) (domain.WorkerRequirements, error) {
	var want domain.WorkerRequirements
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&want); err != nil {
		return want, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return want, errors.New("wantEnv must contain exactly one JSON object")
	}
	return want, nil
}

func exactJavaMatrixWant(want domain.WorkerRequirements) bool {
	allowedLine := map[string]bool{"8": true, "11": true, "17": true, "21": true, "25": true}
	return want.SandboxCapability == domain.CapContainerRun &&
		(want.VerifierAdapter == "maven-java@1" || want.VerifierAdapter == "gradle-java@1") &&
		want.Ecosystem == "maven" && want.Runtime == "java" && allowedLine[want.RuntimeVersion] &&
		want.ExecutionContext == "java" && want.BrowserFamily == "" && want.BrowserMajor == "" &&
		want.Engine == "" && want.EngineVersion == "" && len(want.Frameworks) == 0
}

func (cv *CrossVerifier) canPrepareJob(job Job) bool {
	if !cv.canPrepare(job.WantEnv) {
		return false
	}
	if job.Reason != MatrixJobReason {
		return true
	}
	want, err := parseWorkerRequirements(job.WantEnv)
	return err == nil && exactJavaMatrixWant(want)
}

// canPrepare is deliberately fail-closed. Legacy cross jobs with no
// requirements remain claimable; malformed, unknown, or unsatisfied
// requirements are treated as work for another machine.
func (cv *CrossVerifier) canPrepare(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return true
	}
	want, err := parseWorkerRequirements(raw)
	if err != nil {
		return false
	}
	if want.SandboxCapability != "" && want.SandboxCapability != cv.Cap {
		return false
	}
	// Container work answers the OS question from the daemon's container OS
	// (inside ContainerSupportsRequirementsOn); native work runs on the host.
	if cv.Cap != domain.CapContainerRun && want.OS != "" && !strings.EqualFold(want.OS, cv.Env.OS) {
		return false
	}
	if cv.Cap == domain.CapContainerRun {
		if !sandbox.ContainerSupportsRequirementsOn(cv.ContainerOS, want) {
			return false
		}
	} else if want.Ecosystem != "" {
		if cv.Env.Ecosystem != want.Ecosystem {
			return false
		}
	} else if want.Runtime != "" && cv.Env.Runtime != want.Runtime {
		return false
	}
	for _, required := range want.Frameworks {
		found := false
		for _, available := range cv.Env.Frameworks {
			if strings.EqualFold(strings.TrimSpace(available), strings.TrimSpace(required)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func matrixExecutionManifest(m domain.SampleManifest, raw json.RawMessage) (domain.SampleManifest, error) {
	want, err := parseWorkerRequirements(raw)
	if err != nil || !exactJavaMatrixWant(want) {
		return m, errors.New("verifier: matrix job does not contain exact supported Java requirements")
	}
	if m.VerifierAdapter != want.VerifierAdapter || m.Environment.Ecosystem != want.Ecosystem ||
		m.Environment.Runtime != want.Runtime ||
		(m.Environment.ExecutionContext != "" && m.Environment.ExecutionContext != want.ExecutionContext) {
		return m, errors.New("verifier: matrix requirements do not match the immutable sample manifest")
	}
	lines := map[string]int{"8": 8, "11": 11, "17": 17, "21": 21, "25": 25}
	if target := m.Environment.LanguageVersion; target != "" {
		if lines[target] == 0 || lines[target] > lines[want.RuntimeVersion] {
			return m, fmt.Errorf("verifier: Java language target %q cannot run on JDK %s", target, want.RuntimeVersion)
		}
	}
	// This is an execution-only copy. The unpacked artifact and csx.json stay
	// byte-for-byte immutable, so the rebuilt content-addressed sample id is
	// still the job's sample id.
	m.Environment.RuntimeVersion = want.RuntimeVersion
	m.Environment.ExecutionContext = want.ExecutionContext
	return m, nil
}

// crossExecutionManifest is the manifest a cross job actually runs against.
//
// The manifest records the machine that WROTE the sample. That machine is a
// contributor's laptop as often as a container, and its toolchain moves on
// its own: when one moved to Go 1.27.0 the runner asked for an image nobody
// has, and three claimed jobs died before a container ever started --
// "verifier runtime version \"1.26\" cannot satisfy \"1.27.0\"" -- filing
// receipts that measured nothing. The queue had already been taught not to
// ASK for that line; this is the same statement at the other end, where the
// image is chosen.
//
// The job's requirements are the authority, exactly as they already are for a
// matrix job. Only the two coordinates the queue itself may relax move here;
// everything else the author recorded is left alone, the unpacked artifact and
// its csx.json stay byte-for-byte immutable so the rebuilt sample id is still
// the job's, and the receipt reports the environment the container really had.
// A legacy job with no requirements says nothing, and changes nothing.
func crossExecutionManifest(m domain.SampleManifest, raw json.RawMessage) (domain.SampleManifest, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return m, nil
	}
	want, err := parseWorkerRequirements(raw)
	if err != nil {
		return m, err
	}
	if want.RuntimeVersion != "" {
		m.Environment.RuntimeVersion = want.RuntimeVersion
	}
	if want.ExecutionContext != "" {
		m.Environment.ExecutionContext = want.ExecutionContext
	}
	// Where the job names nothing, what the author recorded stands -- unless
	// no image serves it, which is the case the queue already relaxed to make
	// the job claimable. Dropping unconditionally would quietly downgrade an
	// older job the fleet CAN serve exactly: a browser sample onto a plain
	// Node lane, or a Python 3.14 sample onto 3.12.
	for _, candidate := range laneCandidates(m) {
		if manifestHasALane(candidate) {
			return candidate, nil
		}
	}
	return m, nil
}

// laneCandidates is the manifest itself first, then the same manifest with
// each author-recorded precision the queue is allowed to relax removed.
func laneCandidates(m domain.SampleManifest) []domain.SampleManifest {
	withoutVersion := m
	withoutVersion.Environment.RuntimeVersion = ""
	withoutContext := m
	withoutContext.Environment.ExecutionContext = ""
	bare := withoutVersion
	bare.Environment.ExecutionContext = ""
	return []domain.SampleManifest{m, withoutVersion, withoutContext, bare}
}

// manifestHasALane asks the image registry the same question the runner will.
// The OS is deliberately not part of it: a manifest's OS is the author's host,
// and the job carries the platform when the sample really names one.
func manifestHasALane(m domain.SampleManifest) bool {
	env := m.Environment
	return sandbox.ContainerSupportsRequirements(domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun,
		VerifierAdapter:   m.VerifierAdapter,
		Ecosystem:         env.Ecosystem, Runtime: env.Runtime,
		RuntimeVersion:   env.RuntimeVersion,
		ExecutionContext: env.ExecutionContext,
		BrowserFamily:    env.BrowserFamily, BrowserMajor: env.BrowserMajor,
		Engine: env.Engine, EngineVersion: env.EngineVersion,
	})
}

// RunOne claims and processes at most one cross-verification job. worked is
// true once a job was claimed, including when its verification or receipt
// submission failed; callers use that distinction to report job failures
// separately from queue/network errors.
func (cv *CrossVerifier) RunOne(ctx context.Context) (worked bool, err error) {
	job, err := cv.FetchJob(ctx)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	if _, err := cv.VerifyAndReport(ctx, job); err != nil {
		return true, err
	}
	if cv.OnVerified != nil {
		cv.OnVerified()
	}
	return true, nil
}

// DownloadArtifact returns the sample artifact, verifying that its SHA-256
// equals the content-addressed sampleID BEFORE anything unpacks it. With a
// Source set it walks local CAS → peers → server first; any failure there
// falls back to a direct server download, so a broken peer path can never
// stall verification.
func (cv *CrossVerifier) DownloadArtifact(ctx context.Context, sampleID string) ([]byte, error) {
	return cv.downloadArtifact(ctx, sampleID, 0)
}

func (cv *CrossVerifier) downloadArtifact(ctx context.Context, sampleID string, jobID int64) ([]byte, error) {
	if cv.Source != nil {
		if body, _, err := cv.Source.Fetch(ctx, sampleID); err == nil {
			// An oversized or wrong-hashed body from a peer is a reason to
			// stop trusting THAT peer, not a reason to stop verifying.
			// Returning here contradicted the promise directly above --
			// "a broken peer path can never stall verification" -- and
			// handed any peer on the network a way to block cross
			// verification of any sample by answering with junk.
			//
			// Both cases now fall through to the server, which is the one
			// source whose bytes are checked against the content address
			// below regardless.
			if len(body) <= samples.MaxCompressedBytes {
				// Re-checked even for local hits: a corrupted CAS object
				// must not be what we sign a receipt over.
				if got := domain.SHA256Hex(body); got == sampleID {
					return body, nil
				}
			}
		}
	}
	u := cv.base() + "/v1/samples/" + url.PathEscape(sampleID) + "/artifact"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if jobID > 0 && cv.Ident != nil {
		req.Header.Set(domain.VerificationJobIDHeader, fmt.Sprintf("%d", jobID))
		req.Header.Set(domain.VerificationPeerIDHeader, cv.Ident.PeerID())
	}
	resp, err := cv.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("verifier: download artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("verifier: download artifact: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, samples.MaxCompressedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("verifier: download artifact: %w", err)
	}
	if len(body) > samples.MaxCompressedBytes {
		return nil, fmt.Errorf("verifier: artifact exceeds %d-byte limit", samples.MaxCompressedBytes)
	}
	if got := domain.SHA256Hex(body); got != sampleID {
		return nil, fmt.Errorf("verifier: artifact hash mismatch: got %s, want %s", got, sampleID)
	}
	return body, nil
}

// VerifyAndReport downloads and hash-verifies the job's artifact, unpacks
// it into a fresh temp workspace, runs the verification engine, and posts
// the signed receipt to POST /v1/verifications.
func (cv *CrossVerifier) VerifyAndReport(ctx context.Context, job *Job) (domain.VerificationReceipt, error) {
	var zero domain.VerificationReceipt
	tgz, err := cv.downloadArtifact(ctx, job.SampleID, job.ID)
	if err != nil {
		return zero, err
	}

	dir, err := os.MkdirTemp(cv.WorkDir, "csx-cross-*")
	if err != nil {
		return zero, fmt.Errorf("verifier: workspace: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := samples.Unpack(tgz, dir); err != nil {
		return zero, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "csx.json"))
	if err != nil {
		return zero, fmt.Errorf("verifier: sample has no csx.json: %w", err)
	}
	var m domain.SampleManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return zero, fmt.Errorf("verifier: parse csx.json: %w", err)
	}
	if job.Reason == MatrixJobReason {
		m, err = matrixExecutionManifest(m, job.WantEnv)
	} else {
		m, err = crossExecutionManifest(m, job.WantEnv)
	}
	if err != nil {
		return zero, err
	}

	// RunLogged, not Run: Run drops the stage output on the floor, and this
	// is the one caller that most needs it. A cross-verification workspace is
	// disposable and the receipt keeps only a digest, so a reproducible
	// failure here left nothing at all to read -- two peers hit the same
	// Django 6.1 resolve failure a day apart and neither could be diagnosed.
	receipt, stageLogs, err := RunLogged(ctx, cv.runner(), cv.Cap, dir, m, cv.Ident, cv.Env)
	if err != nil {
		return zero, err
	}
	// Kept locally and never uploaded. A failure to keep them is reported to
	// the caller for its stdout line and nothing more: logs are a diagnostic
	// aid, not a precondition of the evidence this job exists to produce.
	if path, logErr := cv.StageLogs.Keep(receipt.SampleID, stageLogs, stageResults(receipt)); logErr == nil && path != "" {
		cv.noteStageLogs(path)
	}
	if receipt.SampleID != job.SampleID {
		return zero, fmt.Errorf("verifier: rebuilt sample id %s != job sample id %s", receipt.SampleID, job.SampleID)
	}

	// The resolver has just written the dependency tree into this workspace,
	// and the deferred cleanup above is about to delete it along with the
	// workspace. Every verification did this and none of it was ever kept: the
	// network knew a sample for 1,766 of 3,138 public coordinates and a
	// dependency tree for 563, because edges arrived only from people running
	// `csx run` on their own projects.
	//
	// The receipt cannot carry the tree — SigningBytes canonicalises the whole
	// struct, so a new field would make every peer on an older build compute
	// different signing bytes and reject a receipt that is perfectly valid.
	// The caller holds the database, so it reads the tree here and sends it by
	// the wire path that already exists for edges.
	cv.reportResolvedTree(ctx, dir, m, receipt)

	payload, err := json.Marshal(receipt)
	if err != nil {
		return zero, err
	}
	u := cv.base() + "/v1/verifications"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(domain.VerificationJobIDHeader, fmt.Sprintf("%d", job.ID))
	resp, err := cv.client().Do(req)
	if err != nil {
		return zero, fmt.Errorf("verifier: post receipt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, fmt.Errorf("verifier: post receipt: HTTP %d", resp.StatusCode)
	}
	return receipt, nil
}

// RunBudget runs the claim→verify→report loop under the user's idle
// verification budget (goal.md §9.6): "off", "5m", "15m", "idle", or
// "unlimited". "idle" only verifies when no csx run happened within the
// last 10 minutes (mtime of LastActivityFile). With once=true at most one
// job is processed. It returns when the budget is spent, the queue is
// empty, or ctx is done.
func (cv *CrossVerifier) RunBudget(ctx context.Context, budget string, once bool) error {
	var limit time.Duration
	switch budget {
	case "", "off":
		return nil
	case "5m":
		limit = 5 * time.Minute
	case "15m":
		limit = 15 * time.Minute
	case "idle", "unlimited":
		limit = 0 // no time cap; "idle" gates on activity instead
	default:
		return fmt.Errorf("verifier: unknown idle verification budget %q", budget)
	}

	start := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if budget == "idle" && !cv.IsIdle() {
			return nil // user is active; try again on the next tick
		}
		if limit > 0 && time.Since(start) >= limit {
			return nil
		}
		worked, err := cv.RunOne(ctx)
		if err != nil {
			return err
		}
		if !worked {
			return nil
		}
		if once {
			return nil
		}
	}
}

// IsIdle reports whether the last recorded csx run activity is older than
// the idle window. A missing activity file counts as idle.
func (cv *CrossVerifier) IsIdle() bool {
	if cv.LastActivityFile == "" {
		return true
	}
	fi, err := os.Stat(cv.LastActivityFile)
	if err != nil {
		return true
	}
	return time.Since(fi.ModTime()) >= idleWindow
}

// SetStageLogSink installs the callback that announces a kept log. The worker
// uses it to print the path; without one, a failed job left no trace anyone
// could follow.
func (cv *CrossVerifier) SetStageLogSink(fn func(path string)) {
	cv.OnStageLogs = fn
}

func (cv *CrossVerifier) noteStageLogs(path string) {
	if cv.OnStageLogs != nil {
		cv.OnStageLogs(path)
	}
}

// stageResults reads the per-stage verdicts out of a receipt so the log store
// can tell a run worth keeping from a clean one.
func stageResults(r domain.VerificationReceipt) map[string]string {
	out := make(map[string]string, len(r.Stages))
	for k, v := range r.Stages {
		out[k] = string(v)
	}
	return out
}
