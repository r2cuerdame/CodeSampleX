package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestSampleUploadHappyPath(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, map[string]string{
		"src/index.mjs":     "import axios from 'axios';\nexport const post = axios.post;\n",
		"test/contract.mjs": "console.log('contract');\n",
	})
	sampleID := domain.SHA256Hex(artifact)

	resp := postSample(t, srv.URL, manifest, sampleID, artifact, "")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	var created struct{ SampleID, Status string }
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.SampleID != sampleID || created.Status != "PUBLISHED" {
		t.Fatalf("created = %+v", created)
	}

	// A cross-verification job was queued.
	jobs, err := store.JobsForSample(context.Background(), sampleID)
	if err != nil || len(jobs) != 1 || jobs[0].Reason != "cross" || jobs[0].Status != "open" {
		t.Fatalf("jobs = %+v err=%v, want one open cross job", jobs, err)
	}
	var want domain.WorkerRequirements
	if err := json.Unmarshal([]byte(jobs[0].WantEnvJSON), &want); err != nil {
		t.Fatalf("worker requirements = %q: %v", jobs[0].WantEnvJSON, err)
	}
	if want.SandboxCapability != domain.CapContainerRun || want.Ecosystem != "npm" {
		t.Fatalf("worker requirements = %+v, want Docker npm support", want)
	}

	// Metadata reads back with anonymous seeder.
	var meta struct {
		SampleID     string          `json:"sampleId"`
		Status       string          `json:"status"`
		License      string          `json:"license"`
		OriginSeeder string          `json:"originSeeder"`
		Manifest     json.RawMessage `json:"manifest"`
		Receipts     []any           `json:"receipts"`
	}
	getJSON(t, srv.URL+"/v1/samples/"+sampleID, &meta)
	if meta.Status != "PUBLISHED" || meta.License != "MIT-0" || meta.OriginSeeder != "anonymous" {
		t.Fatalf("meta = %+v", meta)
	}
	if len(meta.Manifest) == 0 {
		t.Fatal("meta must embed the manifest")
	}

	// Artifact streams back byte-identical (Main Seeder fallback).
	aresp, err := http.Get(srv.URL + "/v1/samples/" + sampleID + "/artifact")
	if err != nil {
		t.Fatal(err)
	}
	defer aresp.Body.Close()
	if aresp.StatusCode != http.StatusOK {
		t.Fatalf("artifact status = %d", aresp.StatusCode)
	}
	if ct := aresp.Header.Get("Content-Type"); ct != "application/gzip" {
		t.Fatalf("content-type = %q", ct)
	}
	got, _ := io.ReadAll(aresp.Body)
	if !bytes.Equal(got, artifact) {
		t.Fatal("artifact bytes differ")
	}
	if domain.SHA256Hex(got) != sampleID {
		t.Fatal("artifact hash mismatch after round trip")
	}
}

func TestBrowserSampleQueuesExactWorkerRequirements(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	manifest := testManifest()
	manifest.Environment.ExecutionContext = "browser"
	manifest.Environment.BrowserFamily = "chrome"
	manifest.Environment.BrowserMajor = "134"
	manifest.Environment.Engine = "chromium"
	manifest.Environment.EngineVersion = "134"
	artifact := buildArtifact(t, manifest, map[string]string{
		"test/contract.mjs": "console.log('browser contract');\n",
	})
	sampleID := domain.SHA256Hex(artifact)

	resp := postSample(t, srv.URL, manifest, sampleID, artifact, "")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	resp.Body.Close()

	jobs, err := store.JobsForSample(context.Background(), sampleID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v err=%v", jobs, err)
	}
	var want domain.WorkerRequirements
	if err := json.Unmarshal([]byte(jobs[0].WantEnvJSON), &want); err != nil {
		t.Fatal(err)
	}
	if want.ExecutionContext != "browser" || want.BrowserFamily != "chrome" || want.BrowserMajor != "134" {
		t.Errorf("queued browser requirements = %+v", want)
	}
	if want.Engine != "chromium" || want.EngineVersion != "134" {
		t.Errorf("queued engine requirements = %+v", want)
	}
}

func TestPythonSampleQueuesRuntimeVersionRequirement(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	manifest := testManifest()
	manifest.Environment = domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "pypi", Runtime: "python",
		RuntimeVersion: "3.14", ExecutionContext: "python",
	}
	manifest.Packages = []string{"pkg:pypi/example@1.0.0"}
	manifest.Symbols = []string{"example.run"}
	manifest.ContractCommand = []string{"python", "test/contract.py"}
	manifest.VerifierAdapter = "python@1"
	manifest.Case.Packages = append([]string(nil), manifest.Packages...)
	manifest.Case.Symbols = append([]string(nil), manifest.Symbols...)
	manifest.Case.CaseID = ""
	manifest.Case.CaseID = manifest.Case.ComputeID()
	artifact := buildArtifact(t, manifest, map[string]string{
		"test/contract.py": "print('contract')\n",
	})
	sampleID := domain.SHA256Hex(artifact)

	resp := postSample(t, srv.URL, manifest, sampleID, artifact, "")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	resp.Body.Close()
	jobs, err := store.JobsForSample(context.Background(), sampleID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v err=%v", jobs, err)
	}
	var want domain.WorkerRequirements
	if err := json.Unmarshal([]byte(jobs[0].WantEnvJSON), &want); err != nil {
		t.Fatal(err)
	}
	if want.Ecosystem != "pypi" || want.Runtime != "python" || want.RuntimeVersion != "3.14" ||
		want.ExecutionContext != "python" {
		t.Fatalf("queued Python requirements = %+v", want)
	}
}

func TestGradleSampleQueuesExactVerifierAdapterRequirement(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	manifest := testManifest()
	manifest.Environment = domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "maven", Runtime: "java", RuntimeVersion: "21",
		ExecutionContext: "java", PackageManager: "gradle",
	}
	manifest.Packages = []string{"pkg:maven/org.apache.commons/commons-lang3@3.17.0"}
	manifest.Symbols = []string{"StringUtils.isBlank"}
	manifest.BuildCommand = []string{"gradle", "--offline", "--no-daemon", "--no-scan", "--console=plain", "--project-dir", "/work/.csx-vendor/gradle-runner", "classes"}
	manifest.ContractCommand = []string{"gradle", "--offline", "--no-daemon", "--no-scan", "--console=plain", "--project-dir", "/work/.csx-vendor/gradle-runner", "contract"}
	manifest.VerifierAdapter = "gradle-java@1"
	manifest.Case.Packages = append([]string(nil), manifest.Packages...)
	manifest.Case.Symbols = append([]string(nil), manifest.Symbols...)
	manifest.Case.CaseID = ""
	manifest.Case.CaseID = manifest.Case.ComputeID()
	artifact := buildArtifact(t, manifest, map[string]string{
		"src/main/java/BlankChecks.java": "public final class BlankChecks {}\n",
		"test/Contract.java":             "public final class Contract { public static void main(String[] args) {} }\n",
	})
	sampleID := domain.SHA256Hex(artifact)

	resp := postSample(t, srv.URL, manifest, sampleID, artifact, "")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	resp.Body.Close()
	jobs, err := store.JobsForSample(context.Background(), sampleID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v err=%v", jobs, err)
	}
	var want domain.WorkerRequirements
	if err := json.Unmarshal([]byte(jobs[0].WantEnvJSON), &want); err != nil {
		t.Fatal(err)
	}
	if want.VerifierAdapter != "gradle-java@1" || want.Ecosystem != "maven" || want.Runtime != "java" ||
		want.RuntimeVersion != "21" || want.ExecutionContext != "java" {
		t.Fatalf("queued Gradle requirements = %+v", want)
	}
}

// TestRepublishDoesNotStackCrossJobs pins queue hygiene: re-publishing the
// same content must not hand peers the same sandbox work again.
func TestRepublishDoesNotStackCrossJobs(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, map[string]string{
		"src/index.mjs":     "import axios from 'axios';\nexport const post = axios.post;\n",
		"test/contract.mjs": "console.log('contract');\n",
	})
	sampleID := domain.SHA256Hex(artifact)

	for i := 0; i < 3; i++ {
		resp := postSample(t, srv.URL, manifest, sampleID, artifact, "")
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload %d: status %d", i, resp.StatusCode)
		}
	}
	jobs, err := store.JobsForSample(context.Background(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, j := range jobs {
		if j.Reason == "cross" && (j.Status == "open" || j.Status == "claimed") {
			open++
		}
	}
	if open != 1 {
		t.Errorf("open cross jobs = %d after 3 publishes, want 1: %+v", open, jobs)
	}
}

func TestSampleUploadRejectsSampleIDMismatch(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, nil)
	wrongID := "sha256:" + strings.Repeat("00", 32)

	resp := postSample(t, srv.URL, manifest, wrongID, artifact, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "does not match") {
		t.Fatalf("body = %s", body)
	}
}

func TestSampleUploadRejectsStaleCaseID(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	manifest := testManifest()
	manifest.Case.CaseID = "case:sha256:" + strings.Repeat("0", 64)
	artifact := buildArtifact(t, manifest, nil)
	sampleID := domain.SHA256Hex(artifact)

	resp := postSample(t, srv.URL, manifest, sampleID, artifact, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 400", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "caseId does not match") {
		t.Fatalf("body = %s", body)
	}
	if _, ok, err := store.GetSample(context.Background(), sampleID); err != nil || ok {
		t.Fatalf("mismatched case sample was stored: ok=%v err=%v", ok, err)
	}
}

func TestSampleUploadRejectsOversizedArtifact(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()
	// >256KB payload (contents are irrelevant — the size gate fires first).
	big := bytes.Repeat([]byte("0123456789abcdef"), (maxArtifactBytes/16)+64)
	resp := postSample(t, srv.URL, manifest, domain.SHA256Hex(big), big, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "256KB") {
		t.Fatalf("body = %s", body)
	}
}

func TestSampleUploadRejectsForbiddenContents(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()

	cases := []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"forbidden dir", map[string]string{"node_modules/x/index.js": "x"}, "forbidden"},
		{"traversal", map[string]string{"../evil.js": "x"}, "traversal"},
		{"env file", map[string]string{".env": "SECRET=1"}, ".env"},
		{"binary", map[string]string{"blob.bin": "abc\x00def"}, "binary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact := buildArtifact(t, manifest, tc.extra)
			resp := postSample(t, srv.URL, manifest, domain.SHA256Hex(artifact), artifact, "")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), tc.want) {
				t.Fatalf("body = %s, want mention of %q", body, tc.want)
			}
		})
	}
}

func TestSampleUploadRejectsMissingOrMismatchedCsxJSON(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()

	// Artifact whose csx.json differs from the posted manifest.
	other := manifest
	other.Case.Goal = "something else entirely"
	artifact := buildArtifact(t, other, nil)
	resp := postSample(t, srv.URL, manifest, domain.SHA256Hex(artifact), artifact, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "does not match the posted manifest") {
		t.Fatalf("body = %s", body)
	}

	// Artifact without csx.json at all.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte{})
	gz.Close()
	noCsx := buf.Bytes()
	resp = postSample(t, srv.URL, manifest, domain.SHA256Hex(noCsx), noCsx, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSampleUploadRejectsNonPermissiveLicense(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()
	manifest.License = "GPL-3.0-only"
	artifact := buildArtifact(t, manifest, nil)
	resp := postSample(t, srv.URL, manifest, domain.SHA256Hex(artifact), artifact, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "license") {
		t.Fatalf("body = %s", body)
	}
}

func TestSampleUploadRejectsUnknownBearerToken(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, nil)
	resp := postSample(t, srv.URL, manifest, domain.SHA256Hex(artifact), artifact, "csx_bogus")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSampleMetaNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	resp, _ := http.Get(srv.URL + "/v1/samples/sha256:" + strings.Repeat("ff", 32))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// The other half of the same promise: "and none once it is already
// crossed". That branch could never run, because SaveSample overwrote the
// row's status with "PUBLISHED" before queueCrossVerification read it back
// — so a re-publish of a cross-verified sample handed peers a fresh sandbox
// job to re-prove an artifact an independent peer had already reproduced.
//
// An unauthenticated stranger could trigger it: the artifact is public, and
// posting the identical bytes back was enough.
func TestRepublishingACrossedSampleQueuesNoNewWork(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, map[string]string{
		"src/index.mjs":     "import axios from 'axios';\nexport const post = axios.post;\n",
		"test/contract.mjs": "console.log('contract');\n",
	})
	sampleID := domain.SHA256Hex(artifact)

	if resp := postSample(t, srv.URL, manifest, sampleID, artifact, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first upload: status %d", resp.StatusCode)
	}
	ctx := context.Background()
	// Reaching CROSS_PASS happens through peer receipts, which also close
	// the job that asked for the work.
	if err := store.SetSampleStatus(ctx, sampleID, "CROSS_PASS"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteJobsForSample(ctx, sampleID, "ed25519:peer-b"); err != nil {
		t.Fatal(err)
	}
	before, err := store.JobsForSample(ctx, sampleID)
	if err != nil {
		t.Fatal(err)
	}

	if resp := postSample(t, srv.URL, manifest, sampleID, artifact, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-upload: status %d", resp.StatusCode)
	}

	row, ok, err := store.GetSample(ctx, sampleID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if row.Status != "CROSS_PASS" {
		t.Errorf("status after re-publish = %q, want CROSS_PASS", row.Status)
	}
	after, err := store.JobsForSample(ctx, sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("re-publishing a crossed sample queued %d new job(s): %+v",
			len(after)-len(before), after)
	}
	for _, j := range after {
		if j.Reason == "cross" && (j.Status == "open" || j.Status == "claimed") {
			t.Errorf("a cross job is open for an already-crossed sample: %+v", j)
		}
	}
}
