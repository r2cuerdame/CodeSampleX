package verifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

// An empty queue and a queue full of work this machine cannot run look
// identical from the outside: FetchJob returns no job and the worker prints
// completed=0 failed=0. Production sat in the second state for three days
// while every reader believed the first one.
//
// The skip is right — a worker must not claim what it cannot run. What was
// missing is that it happened silently.
func TestFetchJobRemembersWorkNoLaneHere(t *testing.T) {
	ident, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claims := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/verification/jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{
			"id": 6035, "sampleId": "sha256:" + strings.Repeat("a", 64),
			"reason": "cross",
			"wantEnv": map[string]any{
				"sandboxCapability": "CONTAINER_RUN", "verifierAdapter": "golang@1",
				"ecosystem": "golang", "runtime": "go", "runtimeVersion": "1.27",
			},
		}}})
	})
	mux.HandleFunc("POST /v1/verification/jobs/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		claims++
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cv := &CrossVerifier{HTTP: srv.Client(), ServerURL: srv.URL, Ident: ident, Cap: domain.CapContainerRun}
	job, err := cv.FetchJob(context.Background())
	if err != nil || job != nil || claims != 0 {
		t.Fatalf("job=%+v claims=%d err=%v; want the unrunnable row skipped", job, claims, err)
	}
	skipped := cv.UnsupportedWork()
	if len(skipped) != 1 {
		t.Fatalf("UnsupportedWork() = %v, want one coordinate", skipped)
	}
	if !strings.Contains(skipped[0], "golang") || !strings.Contains(skipped[0], "1.27") {
		t.Errorf("UnsupportedWork() = %v, want the coordinate that has no lane", skipped)
	}
}

// A queue that offered nothing reports nothing. "No work" and "no lane for
// the work" must not collapse back into one message.
func TestFetchJobReportsNothingWhenTheQueueIsEmpty(t *testing.T) {
	ident, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/verification/jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cv := &CrossVerifier{HTTP: srv.Client(), ServerURL: srv.URL, Ident: ident, Cap: domain.CapContainerRun}
	if job, err := cv.FetchJob(context.Background()); err != nil || job != nil {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if got := cv.UnsupportedWork(); len(got) != 0 {
		t.Fatalf("UnsupportedWork() = %v, want empty for an empty queue", got)
	}
}

// Making the QUEUE runnable is not enough: the runner picks its image from
// the sample manifest, and that manifest carries the author's own toolchain.
// Production proved it the moment the three stuck jobs were made claimable —
// the farm claimed all three and every one died before entering a container:
//
//	resolve (FAIL)
//	sandbox: verifier runtime version "1.26" cannot satisfy "1.27.0"
//
// So the job's requirements are what a cross run executes against, exactly as
// a matrix job already works. The artifact and its csx.json stay byte-for-byte
// immutable — this is an execution-only copy, the sample id is still rebuilt
// from the files on disk, and the receipt reports the environment the
// container actually had.
func TestACrossRunExecutesTheJobsRequirementsNotTheAuthorsToolchain(t *testing.T) {
	m := domain.SampleManifest{
		SchemaVersion: 1, VerifierAdapter: "golang@1",
		ContractCommand: []string{"go", "test", "./..."},
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "golang", OS: "windows", Runtime: "go",
			RuntimeVersion: "1.27.0", Language: "go", ExecutionContext: "host",
		},
	}
	if img := (sandbox.DockerRunner{}).VerifierImage(m); img != nil {
		t.Fatalf("the author's manifest already selected an image (%v); this test proves nothing", img)
	}

	want := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "golang@1",
		Ecosystem: "golang", Runtime: "go",
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	execManifest, err := crossExecutionManifest(m, raw)
	if err != nil {
		t.Fatalf("cross execution manifest: %v", err)
	}
	img := (sandbox.DockerRunner{}).VerifierImage(execManifest)
	if img == nil {
		t.Fatal("the job's own requirements still selected no verifier image")
	}
	if !strings.Contains(img.Reference, "golang") || !strings.Contains(img.Reference, "@sha256:") {
		t.Errorf("image = %+v, want the digest-pinned Go lane", img)
	}
	// Only the coordinates the job speaks for move. Everything else the
	// author recorded is untouched.
	if execManifest.Environment.OS != "windows" || execManifest.Environment.Language != "go" {
		t.Errorf("execution copy rewrote more than the job asked for: %+v", execManifest.Environment)
	}
}

// A job that pins a line the fleet serves still runs on that line, and a
// legacy job with no requirements at all leaves the manifest exactly as it is.
func TestACrossRunKeepsAPinnedLineAndLeavesLegacyJobsAlone(t *testing.T) {
	m := domain.SampleManifest{
		SchemaVersion: 1, VerifierAdapter: "python@1",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "pypi", Runtime: "python", RuntimeVersion: "3.14.2",
		},
	}
	pinned, err := json.Marshal(domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "python@1",
		Ecosystem: "pypi", Runtime: "python", RuntimeVersion: "3.14",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := crossExecutionManifest(m, pinned)
	if err != nil {
		t.Fatal(err)
	}
	if got.Environment.RuntimeVersion != "3.14" {
		t.Errorf("runtime version = %q, want the pinned line %q", got.Environment.RuntimeVersion, "3.14")
	}

	for _, legacy := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("  ")} {
		got, err := crossExecutionManifest(m, legacy)
		if err != nil {
			t.Fatalf("legacy job %q: %v", legacy, err)
		}
		if got.Environment.RuntimeVersion != "3.14.2" {
			t.Errorf("legacy job %q rewrote the manifest: %q", legacy, got.Environment.RuntimeVersion)
		}
	}
}

// Relaxing is a repair, never a downgrade. A job created before requirements
// carried the execution context says nothing about it, and clearing what the
// author recorded would send browser work to a plain Node lane and sign a
// confident receipt about an environment nobody asked about.
func TestACrossRunNeverDowngradesAManifestTheFleetCanServeExactly(t *testing.T) {
	browser := domain.SampleManifest{
		SchemaVersion: 1, VerifierAdapter: "node-typescript@1",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", Runtime: "node",
			ExecutionContext: "browser", BrowserFamily: "chrome", BrowserMajor: "134",
			Engine: "chromium", EngineVersion: "134",
		},
	}
	legacy, err := json.Marshal(domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "node-typescript@1",
		Ecosystem: "npm", Runtime: "node",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := crossExecutionManifest(browser, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got.Environment.ExecutionContext != "browser" {
		t.Fatalf("execution context = %q, want the author's %q", got.Environment.ExecutionContext, "browser")
	}
	img := (sandbox.DockerRunner{}).VerifierImage(got)
	demoted := browser
	demoted.Environment.ExecutionContext = ""
	if img == nil || img.Reference == "" {
		t.Fatalf("image = %+v, want the pinned browser lane", img)
	}
	if plain := (sandbox.DockerRunner{}).VerifierImage(demoted); plain != nil && plain.Reference == img.Reference {
		t.Fatalf("browser work selected the same image as a context-less run: %s", img.Reference)
	}

	// The same guard on the version: a Python 3.14 sample must not be quietly
	// run on 3.12 because an older job did not name the line.
	py := domain.SampleManifest{
		SchemaVersion: 1, VerifierAdapter: "python@1",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "pypi", Runtime: "python", RuntimeVersion: "3.14",
		},
	}
	legacyPy, err := json.Marshal(domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "python@1",
		Ecosystem: "pypi", Runtime: "python",
	})
	if err != nil {
		t.Fatal(err)
	}
	gotPy, err := crossExecutionManifest(py, legacyPy)
	if err != nil {
		t.Fatal(err)
	}
	if gotPy.Environment.RuntimeVersion != "3.14" {
		t.Fatalf("runtime version = %q, want the author's %q", gotPy.Environment.RuntimeVersion, "3.14")
	}
}
