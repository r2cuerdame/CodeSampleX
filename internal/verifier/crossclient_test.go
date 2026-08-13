package verifier

import (
	"context"
	"encoding/json"
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
	srv        *httptest.Server
	sampleID   string
	artifact   []byte
	mu         sync.Mutex
	jobServed  bool
	claims     []string
	receipts   []domain.VerificationReceipt
	corruptArt bool
	requests   int
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

	// No jobs left → nil, nil.
	job2, err := cv.FetchJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if job2 != nil {
		t.Fatalf("expected no job, got %+v", job2)
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
