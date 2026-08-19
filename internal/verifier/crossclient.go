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
	for i := range body.Jobs {
		job := body.Jobs[i]
		if job.Reason != CrossJobReason && job.Reason != MatrixJobReason {
			continue
		}
		if !cv.canPrepareJob(job) {
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
		if err != nil {
			return zero, err
		}
	}

	receipt, err := Run(ctx, cv.runner(), cv.Cap, dir, m, cv.Ident, cv.Env)
	if err != nil {
		return zero, err
	}
	if receipt.SampleID != job.SampleID {
		return zero, fmt.Errorf("verifier: rebuilt sample id %s != job sample id %s", receipt.SampleID, job.SampleID)
	}

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
