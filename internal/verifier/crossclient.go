package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

// idleWindow: idle-budget verification only proceeds when no csx run
// activity happened within this window (plan P8.3).
const idleWindow = 10 * time.Minute

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
		return sandbox.DockerRunner{}
	}
	return sandbox.NativeRunner{}
}

// FetchJob asks the server for one open job matching this peer's
// capability and claims it via POST /v1/verification/jobs/{id}/claim.
// (nil, nil) means no work is available.
func (cv *CrossVerifier) FetchJob(ctx context.Context) (*Job, error) {
	u := fmt.Sprintf("%s/v1/verification/jobs?peerId=%s&capability=%s&limit=1",
		cv.base(), url.QueryEscape(cv.Ident.PeerID()), url.QueryEscape(string(cv.Cap)))
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
	if len(body.Jobs) == 0 {
		return nil, nil
	}
	job := body.Jobs[0]

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
	defer cresp.Body.Close()
	if cresp.StatusCode < 200 || cresp.StatusCode >= 300 {
		return nil, fmt.Errorf("verifier: claim job %d: HTTP %d", job.ID, cresp.StatusCode)
	}
	return &job, nil
}

// DownloadArtifact returns the sample artifact, verifying that its SHA-256
// equals the content-addressed sampleID BEFORE anything unpacks it. With a
// Source set it walks local CAS → peers → server first; any failure there
// falls back to a direct server download, so a broken peer path can never
// stall verification.
func (cv *CrossVerifier) DownloadArtifact(ctx context.Context, sampleID string) ([]byte, error) {
	if cv.Source != nil {
		if body, _, err := cv.Source.Fetch(ctx, sampleID); err == nil {
			if len(body) > samples.MaxCompressedBytes {
				return nil, fmt.Errorf("verifier: artifact exceeds %d-byte limit", samples.MaxCompressedBytes)
			}
			// Re-check even for local hits: a corrupted CAS object must not
			// be what we sign a receipt over.
			if got := domain.SHA256Hex(body); got == sampleID {
				return body, nil
			}
		}
	}
	u := cv.base() + "/v1/samples/" + url.PathEscape(sampleID) + "/artifact"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
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
	tgz, err := cv.DownloadArtifact(ctx, job.SampleID)
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
		if budget == "idle" && !cv.isIdle() {
			return nil // user is active; try again on the next tick
		}
		if limit > 0 && time.Since(start) >= limit {
			return nil
		}
		job, err := cv.FetchJob(ctx)
		if err != nil {
			return err
		}
		if job == nil {
			return nil
		}
		if _, err := cv.VerifyAndReport(ctx, job); err != nil {
			return err
		}
		if cv.OnVerified != nil {
			cv.OnVerified()
		}
		if once {
			return nil
		}
	}
}

// isIdle reports whether the last recorded csx run activity is older than
// the idle window. A missing activity file counts as idle.
func (cv *CrossVerifier) isIdle() bool {
	if cv.LastActivityFile == "" {
		return true
	}
	fi, err := os.Stat(cv.LastActivityFile)
	if err != nil {
		return true
	}
	return time.Since(fi.ModTime()) >= idleWindow
}
