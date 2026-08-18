package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/samples"
)

// crossFixture builds a real canonical artifact and a server that serves
// the full claim → download → report flow, recording what it saw.
type crossFixture struct {
	srv             *httptest.Server
	sampleID        string
	artifact        []byte
	mu              sync.Mutex
	jobServed       bool
	claims          []string
	receipts        []domain.VerificationReceipt
	receiptJobIDs   []string
	artifactJobIDs  []string
	artifactPeerIDs []string
	corruptArt      bool
	requests        int
	reasonSeen      string
}

func newCrossFixture(t *testing.T) *crossFixture {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/echo.mjs", "export function echo(x){ return x }\n")
	write("test/contract.mjs", "console.log('ok')\n")

	created, err := samples.CreateFromDir(context.Background(), dir, testManifest())
	if err != nil {
		t.Fatal(err)
	}

	f := &crossFixture{sampleID: created.SampleID, artifact: created.Artifact}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/verification/jobs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		f.reasonSeen = r.URL.Query().Get("reason")
		if r.URL.Query().Get("peerId") == "" || r.URL.Query().Get("capability") == "" {
			http.Error(w, "missing peerId/capability", http.StatusBadRequest)
			return
		}
		jobs := []map[string]any{}
		if !f.jobServed {
			f.jobServed = true
			jobs = append(jobs, map[string]any{"id": 7, "sampleId": f.sampleID, "reason": "cross"})
		}
		json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
	})
	mux.HandleFunc("POST /v1/verification/jobs/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		var body struct {
			PeerID string `json:"peerId"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.claims = append(f.claims, r.PathValue("id")+":"+body.PeerID)
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /v1/samples/{id}/artifact", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		f.artifactJobIDs = append(f.artifactJobIDs, r.Header.Get(domain.VerificationJobIDHeader))
		f.artifactPeerIDs = append(f.artifactPeerIDs, r.Header.Get(domain.VerificationPeerIDHeader))
		art := f.artifact
		if f.corruptArt {
			art = append([]byte("corrupt"), art...)
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(art)
	})
	mux.HandleFunc("POST /v1/verifications", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		var rec domain.VerificationReceipt
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.receipts = append(f.receipts, rec)
		f.receiptJobIDs = append(f.receiptJobIDs, r.Header.Get(domain.VerificationJobIDHeader))
		json.NewEncoder(w).Encode(map[string]any{"status": "accepted"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *crossFixture) verifier(t *testing.T) *CrossVerifier {
	t.Helper()
	ident, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &CrossVerifier{
		HTTP:      f.srv.Client(),
		ServerURL: f.srv.URL,
		Ident:     ident,
		Cap:       domain.CapContainerRun,
		Runner:    allPassRunner(),
		Env:       testEnv(),
	}
}

func TestCrossFetchJobClaims(t *testing.T) {
	f := newCrossFixture(t)
	cv := f.verifier(t)

	job, err := cv.FetchJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.ID != 7 || job.SampleID != f.sampleID {
		t.Fatalf("job: %+v", job)
	}
	if len(f.claims) != 1 || f.claims[0] != "7:"+cv.Ident.PeerID() {
		t.Fatalf("claims: %v", f.claims)
	}
	if f.reasonSeen != "" {
		t.Fatalf("job query reason = %q, want both cross and matrix", f.reasonSeen)
	}

	// No jobs left → nil, nil.
	job2, err := cv.FetchJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if job2 != nil {
		t.Fatalf("expected no job, got %+v", job2)
	}
}

func TestMatrixExecutionManifestOverlaysOnlySupportedJavaEnvironment(t *testing.T) {
	m := domain.SampleManifest{
		VerifierAdapter: "gradle-java@1",
		Environment: domain.EnvironmentFingerprint{
			Ecosystem: "maven", Runtime: "java", RuntimeVersion: "21",
			LanguageVersion: "8", ExecutionContext: "java",
		},
	}
	want := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "gradle-java@1",
		Ecosystem: "maven", Runtime: "java", RuntimeVersion: "25", ExecutionContext: "java",
	}
	raw, _ := json.Marshal(want)
	got, err := matrixExecutionManifest(m, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Environment.RuntimeVersion != "25" || got.Environment.LanguageVersion != "8" {
		t.Fatalf("execution manifest env = %+v", got.Environment)
	}
	if m.Environment.RuntimeVersion != "21" {
		t.Fatal("matrix overlay mutated the artifact manifest value")
	}

	want.RuntimeVersion = "8"
	m.Environment.LanguageVersion = "11"
	raw, _ = json.Marshal(want)
	if _, err := matrixExecutionManifest(m, raw); err == nil {
		t.Fatal("JDK 8 accepted a Java 11 language target")
	}

	var injected map[string]any
	if err := json.Unmarshal(raw, &injected); err != nil {
		t.Fatal(err)
	}
	injected["runtimeVersion"] = "8; throw new Error()"
	raw, _ = json.Marshal(injected)
	if _, err := matrixExecutionManifest(m, raw); err == nil {
		t.Fatal("non-numeric/injected Java line was accepted")
	}
}

func TestCrossFetchSkipsMatrixJobEvenIfServerIgnoresFilter(t *testing.T) {
	ident, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claims := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/verification/jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{
			"id": 9, "sampleId": "sha256:" + strings.Repeat("a", 64),
			"reason": "matrix", "wantEnv": map[string]any{"os": "darwin"},
		}}})
	})
	mux.HandleFunc("POST /v1/verification/jobs/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		claims++
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cv := &CrossVerifier{
		HTTP: srv.Client(), ServerURL: srv.URL, Ident: ident,
		Cap: domain.CapContainerRun,
	}
	if job, err := cv.FetchJob(context.Background()); err != nil || job != nil {
		t.Fatalf("matrix job result = %+v, %v; want no claimable work", job, err)
	}
	if claims != 0 {
		t.Fatalf("matrix job was claimed %d times", claims)
	}
}

func TestWorkerClaimsOnlyCompleteSupportedJavaMatrix(t *testing.T) {
	cv := &CrossVerifier{Cap: domain.CapContainerRun}
	want := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "maven-java@1",
		Ecosystem: "maven", Runtime: "java", RuntimeVersion: "17", ExecutionContext: "java",
	}
	raw, _ := json.Marshal(want)
	if !cv.canPrepareJob(Job{Reason: MatrixJobReason, WantEnv: raw}) {
		t.Fatal("worker rejected a complete pinned Java matrix job")
	}
	partial := json.RawMessage(`{"runtime":"java","runtimeMajor":"17"}`)
	if cv.canPrepareJob(Job{Reason: MatrixJobReason, WantEnv: partial}) {
		t.Fatal("worker accepted a legacy partial matrix job")
	}
	want.RuntimeVersion = "22"
	raw, _ = json.Marshal(want)
	if cv.canPrepareJob(Job{Reason: MatrixJobReason, WantEnv: raw}) {
		t.Fatal("worker accepted an unpinned Java line")
	}
}

func TestCrossFetchSkipsUnsatisfiedWantEnvOnCrossJob(t *testing.T) {
	ident, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claims := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/verification/jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{
			"id": 10, "sampleId": "sha256:" + strings.Repeat("b", 64),
			"reason": "cross", "wantEnv": map[string]any{"runtime": "node", "runtimeVersion": "20"},
		}}})
	})
	mux.HandleFunc("POST /v1/verification/jobs/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		claims++
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cv := &CrossVerifier{
		HTTP: srv.Client(), ServerURL: srv.URL, Ident: ident,
		Cap: domain.CapContainerRun,
	}
	if job, err := cv.FetchJob(context.Background()); err != nil || job != nil {
		t.Fatalf("targeted cross job result = %+v, %v; want no claimable work", job, err)
	}
	if claims != 0 {
		t.Fatalf("environment-targeted job was claimed %d times", claims)
	}
}

func TestCrossFetchSkipsUnpreparedEngineAndClaimsLaterContainerJob(t *testing.T) {
	ident, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var claimed string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/verification/jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{
			{
				"id": 11, "sampleId": "sha256:" + strings.Repeat("c", 64), "reason": "cross",
				"wantEnv": map[string]any{"sandboxCapability": "CONTAINER_RUN", "frameworks": []string{"unity@6000.0.24f1"}},
			},
			{
				"id": 12, "sampleId": "sha256:" + strings.Repeat("d", 64), "reason": "cross",
				"wantEnv": map[string]any{"sandboxCapability": "CONTAINER_RUN", "ecosystem": "npm", "runtime": "node"},
			},
		}})
	})
	mux.HandleFunc("POST /v1/verification/jobs/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		claimed = r.PathValue("id")
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cv := &CrossVerifier{
		HTTP: srv.Client(), ServerURL: srv.URL, Ident: ident,
		Cap: domain.CapContainerRun,
		Env: domain.EnvironmentFingerprint{Frameworks: nil},
	}
	job, err := cv.FetchJob(context.Background())
	if err != nil || job == nil || job.ID != 12 || claimed != "12" {
		t.Fatalf("job = %+v, claimed = %q, err = %v; want compatible job 12", job, claimed, err)
	}
}

func TestCrossWorkerClaimsOnlyThePinnedBrowserItCanPrepare(t *testing.T) {
	cv := &CrossVerifier{Cap: domain.CapContainerRun}
	encode := func(w domain.WorkerRequirements) json.RawMessage {
		b, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	base := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun,
		Ecosystem:         "npm", Runtime: "node", ExecutionContext: "browser",
		BrowserFamily: "chrome", BrowserMajor: "134", Engine: "chromium", EngineVersion: "134",
	}
	if !cv.canPrepare(encode(base)) {
		t.Fatal("worker rejected its pinned Chrome 134 container")
	}
	newer := base
	newer.BrowserMajor = "135"
	newer.EngineVersion = "135"
	if cv.canPrepare(encode(newer)) {
		t.Fatal("worker claimed an unconfigured Chrome 135 job")
	}
	firefox := base
	firefox.BrowserFamily = "firefox"
	firefox.Engine = "gecko"
	if cv.canPrepare(encode(firefox)) {
		t.Fatal("worker claimed a Firefox job without a Firefox image")
	}
}

func TestCrossDownloadArtifactVerifiesHash(t *testing.T) {
	f := newCrossFixture(t)
	cv := f.verifier(t)

	got, err := cv.DownloadArtifact(context.Background(), f.sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if domain.SHA256Hex(got) != f.sampleID {
		t.Fatal("downloaded artifact hash mismatch")
	}

	f.corruptArt = true
	if _, err := cv.DownloadArtifact(context.Background(), f.sampleID); err == nil {
		t.Fatal("corrupted artifact must be rejected before unpack")
	} else if !strings.Contains(err.Error(), "hash") {
		t.Fatalf("error should mention hash: %v", err)
	}
}

// stubSource is a canned ArtifactSource: peer.Node in production.
type stubSource struct {
	data  []byte
	err   error
	calls int
}

func (s *stubSource) Fetch(context.Context, string) ([]byte, string, error) {
	s.calls++
	return s.data, "peer", s.err
}

// TestCrossDownloadPrefersSource pins goal.md §15.1: the verifier asks the
// local CAS → peer chain before the seeder, and a source that fails or
// returns wrong bytes silently degrades to the server rather than failing
// the job.
func TestCrossDownloadPrefersSource(t *testing.T) {
	f := newCrossFixture(t)

	// 1. Source hit: server is never touched.
	src := &stubSource{data: f.artifact}
	cv := f.verifier(t)
	cv.Source = src
	cv.ServerURL = "http://127.0.0.1:1" // any server call would fail here
	got, err := cv.DownloadArtifact(context.Background(), f.sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if domain.SHA256Hex(got) != f.sampleID {
		t.Fatal("artifact from source failed hash check")
	}
	if src.calls != 1 {
		t.Fatalf("source calls = %d, want 1", src.calls)
	}

	// 2. Source miss and 3. source returning the wrong bytes both fall back
	// to the server instead of failing.
	for name, bad := range map[string]*stubSource{
		"miss":       {err: errors.New("no peer had it")},
		"wrong-hash": {data: []byte("not the artifact")},
	} {
		t.Run(name, func(t *testing.T) {
			cv := f.verifier(t)
			cv.Source = bad
			got, err := cv.DownloadArtifact(context.Background(), f.sampleID)
			if err != nil {
				t.Fatalf("fallback to server failed: %v", err)
			}
			if domain.SHA256Hex(got) != f.sampleID {
				t.Fatal("server fallback returned the wrong artifact")
			}
		})
	}
}

func TestCrossVerifyAndReport(t *testing.T) {
	f := newCrossFixture(t)
	cv := f.verifier(t)

	job, err := cv.FetchJob(context.Background())
	if err != nil || job == nil {
		t.Fatalf("fetch: %v %v", job, err)
	}
	receipt, err := cv.VerifyAndReport(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SampleID != f.sampleID {
		t.Fatalf("receipt sample id %s, want %s", receipt.SampleID, f.sampleID)
	}
	if len(f.receipts) != 1 {
		t.Fatalf("server received %d receipts", len(f.receipts))
	}
	if len(f.receiptJobIDs) != 1 || f.receiptJobIDs[0] != "7" {
		t.Fatalf("receipt job ids = %v, want exact claimed job 7", f.receiptJobIDs)
	}
	if len(f.artifactJobIDs) != 1 || f.artifactJobIDs[0] != "7" || len(f.artifactPeerIDs) != 1 || f.artifactPeerIDs[0] != cv.Ident.PeerID() {
		t.Fatalf("artifact claim headers = jobs %v peers %v", f.artifactJobIDs, f.artifactPeerIDs)
	}
	posted := f.receipts[0]
	if posted.SampleID != f.sampleID || posted.Stages["contract"] != "PASS" {
		t.Fatalf("posted receipt: %+v", posted)
	}
	if !identity.Verify(posted.PeerPubkey, posted.PeerSignature, posted.SigningBytes()) {
		t.Fatal("posted receipt signature must verify")
	}
}

func TestCrossRunBudgetOff(t *testing.T) {
	f := newCrossFixture(t)
	cv := f.verifier(t)

	if err := cv.RunBudget(context.Background(), "off", true); err != nil {
		t.Fatal(err)
	}
	if f.requests != 0 {
		t.Fatalf("budget off must not touch the network (%d requests)", f.requests)
	}
}

func TestCrossRunBudgetOnce(t *testing.T) {
	f := newCrossFixture(t)
	cv := f.verifier(t)

	if err := cv.RunBudget(context.Background(), "5m", true); err != nil {
		t.Fatal(err)
	}
	if len(f.receipts) != 1 {
		t.Fatalf("once budget run should post exactly 1 receipt, got %d", len(f.receipts))
	}
}

func TestCrossRunBudgetUnlimitedDrainsQueue(t *testing.T) {
	f := newCrossFixture(t)
	cv := f.verifier(t)

	if err := cv.RunBudget(context.Background(), "unlimited", false); err != nil {
		t.Fatal(err)
	}
	if len(f.receipts) != 1 {
		t.Fatalf("expected the single queued job verified, got %d receipts", len(f.receipts))
	}
}

func TestCrossRunBudgetIdle(t *testing.T) {
	f := newCrossFixture(t)
	cv := f.verifier(t)

	activity := filepath.Join(t.TempDir(), "last-activity")
	if err := os.WriteFile(activity, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cv.LastActivityFile = activity

	// Fresh activity (< 10 min) → not idle → nothing happens.
	if err := cv.RunBudget(context.Background(), "idle", true); err != nil {
		t.Fatal(err)
	}
	if f.requests != 0 {
		t.Fatalf("recent activity must suppress idle verification (%d requests)", f.requests)
	}

	// Stale activity (> 10 min) → idle → the job runs.
	old := time.Now().Add(-20 * time.Minute)
	if err := os.Chtimes(activity, old, old); err != nil {
		t.Fatal(err)
	}
	if err := cv.RunBudget(context.Background(), "idle", true); err != nil {
		t.Fatal(err)
	}
	if len(f.receipts) != 1 {
		t.Fatalf("stale activity should allow verification, got %d receipts", len(f.receipts))
	}
}

func TestCrossRunBudgetRejectsGarbage(t *testing.T) {
	f := newCrossFixture(t)
	cv := f.verifier(t)
	if err := cv.RunBudget(context.Background(), "sometimes", true); err == nil {
		t.Fatal("unknown budget must error")
	}
}
