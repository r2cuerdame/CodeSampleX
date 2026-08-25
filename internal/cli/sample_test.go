package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// captureSampleIO swaps the sample command's stdio seams for buffers and
// restores them on cleanup. Returned buffers hold stdout and stderr.
func captureSampleIO(t *testing.T, stdin string) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	outBuf, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	oldOut, oldErr, oldIn := sampleStdout, sampleStderr, sampleStdin
	sampleStdout, sampleStderr = outBuf, errBuf
	sampleStdin = strings.NewReader(stdin)
	t.Cleanup(func() {
		sampleStdout, sampleStderr, sampleStdin = oldOut, oldErr, oldIn
	})
	return outBuf, errBuf
}

func bypassPublishApproval(t *testing.T) {
	t.Helper()
	old := publishApprovalForTest
	publishApprovalForTest = func() bool { return true }
	t.Cleanup(func() { publishApprovalForTest = old })
}

// sampleFixtureDir writes a minimal clean-room sample directory with a
// csx.json manifest. extraFiles maps relative path → content.
func sampleFixtureDir(t *testing.T, extraFiles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1,
			Kind:          "HOW",
			Goal:          "post a JSON body with axios",
			Packages:      []string{"pkg:npm/axios@1.12.0"},
			Contract:      []string{"posts a JSON body and receives the echo"},
		},
		Packages:        []string{"pkg:npm/axios@1.12.0"},
		Symbols:         []string{"axios.post"},
		Environment:     domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64"},
		ContractCommand: []string{"node", "test/contract.mjs"},
		VerifierAdapter: "node-typescript@1",
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"csx.json":          string(raw),
		"index.mjs":         "import axios from 'axios'\nexport const post = (u, b) => axios.post(u, b)\n",
		"test/contract.mjs": "process.exit(0)\n",
	}
	for p, c := range extraFiles {
		files[p] = c
	}
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// createLocalSample runs `csx sample create` on a fixture dir and returns
// the created sample's id (read back from the local DB).
func createLocalSample(t *testing.T, home string, extraFiles map[string]string) string {
	t.Helper()
	dir := sampleFixtureDir(t, extraFiles)
	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "create", dir}); code != 0 {
		t.Fatalf("sample create exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	db := openLocalDB(t, home)
	defer db.Close()
	rows, err := db.ListSamples(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 local sample, got %d", len(rows))
	}
	if !rows[0].HasArtifact {
		t.Fatal("created sample row has has_artifact = 0, want 1")
	}
	return rows[0].SampleID
}

func openLocalDB(t *testing.T, home string) *localdb.DB {
	t.Helper()
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// fakeVerifyRunner is an all-PASS sandbox.Runner used through the
// verifierRunner seam.
type fakeVerifyRunner struct{ contract string }

func (f fakeVerifyRunner) Resolve(context.Context, string, domain.SampleManifest) sandbox.StageResult {
	return sandbox.StageResult{Result: sandbox.ResultPass, Log: "fake resolve"}
}
func (f fakeVerifyRunner) Build(context.Context, string, domain.SampleManifest) sandbox.StageResult {
	return sandbox.StageResult{Result: sandbox.ResultPass, Log: "fake build"}
}
func (f fakeVerifyRunner) Contract(context.Context, string, domain.SampleManifest) sandbox.StageResult {
	return sandbox.StageResult{Result: f.contract, Log: "fake contract"}
}
func (f fakeVerifyRunner) StageEnvironment(host domain.EnvironmentFingerprint, _ domain.SampleManifest) domain.EnvironmentFingerprint {
	return host.Normalize()
}
func (f fakeVerifyRunner) VerifierImage(domain.SampleManifest) *domain.VerifierImage { return nil }

func setVerifierSeams(t *testing.T, r sandbox.Runner, cap domain.SandboxCapability) {
	t.Helper()
	oldRunner, oldCap := verifierRunner, verifierCapability
	verifierRunner, verifierCapability = r, cap
	t.Cleanup(func() { verifierRunner, verifierCapability = oldRunner, oldCap })
}

// publishServer is an httptest server that records what POST /v1/samples
// received.
type publishRecord struct {
	calls       int32
	auth        string
	sampleID    string
	manifest    string
	artifactLen int
	artifactOK  bool
}

func newPublishServer(t *testing.T, rec *publishRecord) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/samples", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&rec.calls, 1)
		rec.auth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rec.sampleID = r.FormValue("sampleId")
		rec.manifest = r.FormValue("manifest")
		f, _, err := r.FormFile("artifact")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer f.Close()
		body, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rec.artifactLen = len(body)
		rec.artifactOK = domain.SHA256Hex(body) == rec.sampleID
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"sampleId":"` + rec.sampleID + `","status":"PUBLISHED"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSampleUsageErrors(t *testing.T) {
	t.Setenv("CSX_HOME", t.TempDir())
	captureSampleIO(t, "")
	if code := Main([]string{"sample"}); code != 2 {
		t.Fatalf("bare `csx sample` exited %d, want 2", code)
	}
	if code := Main([]string{"sample", "bogus"}); code != 2 {
		t.Fatalf("unknown subcommand exited %d, want 2", code)
	}
	if code := Main([]string{"sample", "propose"}); code != 2 {
		t.Fatalf("propose without --goal exited %d, want 2", code)
	}
}

func TestSampleProposeWritesWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	t.Chdir(t.TempDir())
	out, errBuf := captureSampleIO(t, "")

	code := Main([]string{"sample", "propose",
		"--goal", "post a JSON body with axios",
		"--package", "pkg:npm/axios@1.12.0",
		"--symbol", "axios.post"})
	if code != 0 {
		t.Fatalf("propose exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}

	workBase := filepath.Join(home, "samples", "work")
	entries, err := os.ReadDir(workBase)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one clean-room workspace under %s: %v, %v", workBase, entries, err)
	}
	work := filepath.Join(workBase, entries[0].Name())

	specRaw, err := os.ReadFile(filepath.Join(work, "spec.json"))
	if err != nil {
		t.Fatalf("spec.json missing: %v", err)
	}
	var spec samples.SanitizedSpec
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Goal != "post a JSON body with axios" || len(spec.Packages) != 1 || spec.Packages[0] != "pkg:npm/axios@1.12.0" {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	prompt, err := os.ReadFile(filepath.Join(work, "PROMPT.md"))
	if err != nil {
		t.Fatalf("PROMPT.md missing: %v", err)
	}
	if !strings.Contains(string(prompt), "pkg:npm/axios@1.12.0") {
		t.Fatal("PROMPT.md does not mention the package")
	}
	if !strings.Contains(string(prompt), "csx.json manifest scaffold already exists") {
		t.Fatal("PROMPT.md does not tell the agent to complete the scaffold")
	}
	manifestRaw, err := os.ReadFile(filepath.Join(work, "csx.json"))
	if err != nil {
		t.Fatalf("csx.json scaffold missing: %v", err)
	}
	var manifest domain.SampleManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Case.Goal != spec.Goal || manifest.VerifierAdapter != "node-typescript@1" ||
		len(manifest.ContractCommand) == 0 || len(manifest.Case.Contract) != 0 {
		t.Fatalf("incomplete manifest scaffold: %+v", manifest)
	}
	// No llmCommand configured → the user's own agent generates the sample.
	got := out.String()
	if !strings.Contains(got, work) {
		t.Fatalf("output does not print the workspace path %q:\n%s", work, got)
	}
	if !strings.Contains(got, "local LLM") && !strings.Contains(got, "agent") {
		t.Fatalf("output does not instruct that the user's agent/LLM generates the sample:\n%s", got)
	}
}

func TestProposalManifestDistinguishesMavenAndGradleAdapters(t *testing.T) {
	spec := samples.SanitizedSpec{SchemaVersion: 1, Goal: "prove Java API", Kind: "HOW", Packages: []string{"pkg:maven/org.example/lib@1.0.0"}}
	maven := samples.ProposalManifest(spec, domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "maven", PackageManager: "maven"})
	gradle := samples.ProposalManifest(spec, domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "maven", PackageManager: "gradle"})
	if maven.VerifierAdapter != "maven-java@1" || gradle.VerifierAdapter != "gradle-java@1" {
		t.Fatalf("adapters: maven=%q gradle=%q", maven.VerifierAdapter, gradle.VerifierAdapter)
	}
	if maven.Case.Goal != spec.Goal || gradle.Case.Goal != spec.Goal {
		t.Fatal("proposal goal was not preserved in Java scaffold")
	}
}

func TestSampleCreatePreviewVerifyHappyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	sampleID := createLocalSample(t, home, nil)

	// Preview shows everything: manifest, license, file list AND contents.
	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "preview", sampleID}); code != 0 {
		t.Fatalf("preview exited %d\nstderr: %s", code, errBuf)
	}
	got := out.String()
	for _, want := range []string{
		sampleID,
		"MIT-0",
		"post a JSON body with axios",
		"index.mjs",
		"axios.post(u, b)",    // full file content, nothing hidden
		"process.exit(0)",     // contract file content
		"csx.json",            // manifest travels in the artifact
		"Leakage findings: 0", // re-scan result
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("preview output missing %q:\n%s", want, got)
		}
	}

	// Verify via the seam with an all-PASS fake runner.
	setVerifierSeams(t, fakeVerifyRunner{contract: sandbox.ResultPass}, domain.CapContainerRun)
	out, errBuf = captureSampleIO(t, "")
	if code := Main([]string{"sample", "verify", sampleID}); code != 0 {
		t.Fatalf("verify exited %d\nstderr: %s", code, errBuf)
	}
	got = out.String()
	for _, want := range []string{"resolve", "contract", "PASS", string(domain.CapContainerRun)} {
		if !strings.Contains(got, want) {
			t.Fatalf("verify output missing %q:\n%s", want, got)
		}
	}

	db := openLocalDB(t, home)
	defer db.Close()
	receipts, err := db.ReceiptsForSample(context.Background(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 {
		t.Fatalf("expected 1 saved receipt, got %d", len(receipts))
	}
	if receipts[0].Stages["contract"] != "PASS" {
		t.Fatalf("receipt contract stage = %q, want PASS", receipts[0].Stages["contract"])
	}
	row, ok, err := db.GetSample(context.Background(), sampleID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if row.Status != "LOCAL_PASS" {
		t.Fatalf("status after contract-PASS verify = %q, want LOCAL_PASS", row.Status)
	}
}

func TestSampleCreateAgainPreservesEarnedStatusAndReceipt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	dir := sampleFixtureDir(t, nil)

	create := func() {
		t.Helper()
		out, errBuf := captureSampleIO(t, "")
		if code := Main([]string{"sample", "create", dir}); code != 0 {
			t.Fatalf("sample create exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
		}
	}
	create()

	db := openLocalDB(t, home)
	rows, err := db.ListSamples(context.Background())
	if err != nil || len(rows) != 1 {
		db.Close()
		t.Fatalf("created rows = %d, err = %v", len(rows), err)
	}
	sampleID := rows[0].SampleID
	db.Close()

	setVerifierSeams(t, fakeVerifyRunner{contract: sandbox.ResultPass}, domain.CapContainerRun)
	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "verify", sampleID}); code != 0 {
		t.Fatalf("sample verify exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}

	assertState := func(wantStatus string) {
		t.Helper()
		db := openLocalDB(t, home)
		defer db.Close()
		row, ok, err := db.GetSample(context.Background(), sampleID)
		if err != nil || !ok {
			t.Fatalf("GetSample: found=%v err=%v", ok, err)
		}
		if row.Status != wantStatus {
			t.Fatalf("status after repeated create = %q, want %q", row.Status, wantStatus)
		}
		receipts, err := db.ReceiptsForSample(context.Background(), sampleID)
		if err != nil || len(receipts) != 1 || receipts[0].Stages["contract"] != "PASS" {
			t.Fatalf("receipts after repeated create = %+v, err=%v", receipts, err)
		}
	}

	// Re-creating the exact artifact used to overwrite LOCAL_PASS with LOCAL.
	create()
	assertState("LOCAL_PASS")

	// The same local operation must not walk a published sample back into a
	// draft state either; its receipt remains attached to the content id.
	db = openLocalDB(t, home)
	if err := db.SetSampleStatus(context.Background(), sampleID, "PUBLISHED"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	create()
	assertState("PUBLISHED")
}

func TestSampleRemoveRequiresExactIDAndPreservesSharedState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	ctx := context.Background()

	create := func(dir string) {
		t.Helper()
		out, errBuf := captureSampleIO(t, "")
		if code := Main([]string{"sample", "create", dir}); code != 0 {
			t.Fatalf("sample create exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
		}
	}
	create(sampleFixtureDir(t, map[string]string{"variant.txt": "remove me\n"}))

	db := openLocalDB(t, home)
	rows, err := db.ListSamples(ctx)
	if err != nil || len(rows) != 1 {
		db.Close()
		t.Fatalf("initial samples = %d, err = %v", len(rows), err)
	}
	removeID, sharedCaseID := rows[0].SampleID, rows[0].CaseID
	db.Close()

	setVerifierSeams(t, fakeVerifyRunner{contract: sandbox.ResultPass}, domain.CapContainerRun)
	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "verify", removeID}); code != 0 {
		t.Fatalf("sample verify exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}

	// A distinct artifact proves the same case. Removing the first artifact
	// must leave this sample, the shared case row, and its CAS object intact.
	create(sampleFixtureDir(t, map[string]string{"variant.txt": "keep me\n"}))
	db = openLocalDB(t, home)
	rows, err = db.ListSamples(ctx)
	if err != nil || len(rows) != 2 {
		db.Close()
		t.Fatalf("samples before removal = %d, err = %v", len(rows), err)
	}
	var keepID string
	for _, row := range rows {
		if row.SampleID != removeID {
			keepID = row.SampleID
			if row.CaseID != sharedCaseID {
				db.Close()
				t.Fatalf("second artifact case = %q, want shared case %q", row.CaseID, sharedCaseID)
			}
		}
	}
	db.Close()

	store, err := cas.Open(filepath.Join(home, "cas"))
	if err != nil {
		t.Fatal(err)
	}
	if !store.Has(removeID) || !store.Has(keepID) {
		t.Fatalf("expected both CAS objects before removal: remove=%v keep=%v", store.Has(removeID), store.Has(keepID))
	}

	// Prefixes are never resolved for a destructive command.
	out, errBuf = captureSampleIO(t, "")
	if code := Main([]string{"sample", "remove", removeID[:20]}); code != 2 {
		t.Fatalf("prefix removal exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	if !strings.Contains(errBuf.String(), "exactly sha256:") {
		t.Fatalf("prefix rejection did not explain exact-id requirement: %s", errBuf)
	}

	// The typed confirmation is also exact; a mismatch changes nothing.
	out, errBuf = captureSampleIO(t, keepID+"\n")
	if code := Main([]string{"sample", "remove", removeID}); code != 1 {
		t.Fatalf("mismatched confirmation exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	db = openLocalDB(t, home)
	if _, ok, err := db.GetSample(ctx, removeID); err != nil || !ok {
		db.Close()
		t.Fatalf("mismatched confirmation changed sample: found=%v err=%v", ok, err)
	}
	receipts, err := db.ReceiptsForSample(ctx, removeID)
	db.Close()
	if err != nil || len(receipts) != 1 {
		t.Fatalf("mismatched confirmation changed receipts: count=%d err=%v", len(receipts), err)
	}
	if !store.Has(removeID) {
		t.Fatal("mismatched confirmation deleted CAS object")
	}

	out, errBuf = captureSampleIO(t, removeID+"\n")
	if code := Main([]string{"sample", "remove", removeID}); code != 0 {
		t.Fatalf("sample remove exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}

	db = openLocalDB(t, home)
	defer db.Close()
	if _, ok, err := db.GetSample(ctx, removeID); err != nil || ok {
		t.Fatalf("removed sample: found=%v err=%v", ok, err)
	}
	receipts, err = db.ReceiptsForSample(ctx, removeID)
	if err != nil || len(receipts) != 0 {
		t.Fatalf("removed sample receipts = %d, err=%v", len(receipts), err)
	}
	if _, ok, err := db.GetSample(ctx, keepID); err != nil || !ok {
		t.Fatalf("unrelated sample: found=%v err=%v", ok, err)
	}
	if _, ok, err := db.GetCase(ctx, sharedCaseID); err != nil || !ok {
		t.Fatalf("shared case: found=%v err=%v", ok, err)
	}
	hits, err := db.FTSQuery(ctx, "post JSON body axios", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.DocID == removeID || hit.DocID == "sample:"+removeID {
			t.Fatalf("removed sample remains searchable as %q", hit.DocID)
		}
	}
	if store.Has(removeID) {
		t.Fatal("removed sample CAS object still exists")
	}
	if !store.Has(keepID) {
		t.Fatal("unrelated sample CAS object was deleted")
	}

	missingID := "sha256:" + strings.Repeat("0", 64)
	out, errBuf = captureSampleIO(t, missingID+"\n")
	if code := Main([]string{"sample", "remove", missingID}); code != 1 {
		t.Fatalf("missing removal exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	if !strings.Contains(errBuf.String(), "not found") {
		t.Fatalf("missing removal did not clearly report not found: %s", errBuf)
	}
	if _, ok, err := db.GetSample(ctx, keepID); err != nil || !ok || !store.Has(keepID) {
		t.Fatalf("missing removal changed unrelated sample: found=%v cas=%v err=%v", ok, store.Has(keepID), err)
	}
}

func TestSampleVerifyCompileOnlyHonesty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	sampleID := createLocalSample(t, home, nil)

	setVerifierSeams(t, fakeVerifyRunner{contract: sandbox.ResultSkipped}, domain.CapCompileOnly)
	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "verify", sampleID}); code != 0 {
		t.Fatalf("verify exited %d\nstderr: %s", code, errBuf)
	}
	got := out.String()
	if !strings.Contains(got, "COMPILE_ONLY") || !strings.Contains(got, "SKIPPED") {
		t.Fatalf("COMPILE_ONLY verify output must admit the skipped contract:\n%s", got)
	}
	// The note has to say two things, however it words them: that the
	// contract did not run, and that the receipt is therefore not evidence
	// about the sample. Asserting the old sentence letter-for-letter made
	// this fail against a note that says both more plainly than before.
	low := strings.ToLower(got)
	for _, admission := range []string{"did not run", "proves nothing"} {
		if !strings.Contains(low, admission) {
			t.Fatalf("capability note never admits %q:\n%s", admission, got)
		}
	}
}

func TestSampleVerifyJSONPrintsOnlyTheSignedReceipt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	sampleID := createLocalSample(t, home, nil)

	setVerifierSeams(t, fakeVerifyRunner{contract: sandbox.ResultPass}, domain.CapContainerRun)
	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "verify", sampleID, "--json"}); code != 0 {
		t.Fatalf("verify --json exited %d\nstderr: %s", code, errBuf)
	}
	var receipt domain.VerificationReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatalf("stdout is not one receipt JSON document: %v\n%s", err, out.String())
	}
	if receipt.SampleID != sampleID || receipt.Stages["contract"] != sandbox.ResultPass {
		t.Fatalf("receipt = sample %q, contract %q", receipt.SampleID, receipt.Stages["contract"])
	}
	if receipt.PeerSignature == "" {
		t.Fatal("machine output omitted the receipt signature")
	}
	if strings.Contains(out.String(), "Stage      Result") || strings.Contains(out.String(), "Verified ") {
		t.Fatalf("human output contaminated --json stdout:\n%s", out.String())
	}
}

func TestParseSampleVerifyArgs(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		wantID   string
		wantJSON bool
		wantOK   bool
	}{
		{args: []string{"sha256:abc"}, wantID: "sha256:abc", wantOK: true},
		{args: []string{"sha256:abc", "--json"}, wantID: "sha256:abc", wantJSON: true, wantOK: true},
		{args: []string{"--json", "sha256:abc"}, wantID: "sha256:abc", wantJSON: true, wantOK: true},
		{args: nil},
		{args: []string{"--json"}},
		{args: []string{"sha256:a", "sha256:b"}},
		{args: []string{"sha256:a", "--json", "--json"}},
		{args: []string{"sha256:a", "--unknown"}},
	} {
		gotID, gotJSON, gotOK := parseSampleVerifyArgs(tc.args)
		if gotID != tc.wantID || gotJSON != tc.wantJSON || gotOK != tc.wantOK {
			t.Errorf("parseSampleVerifyArgs(%v) = %q, %v, %v; want %q, %v, %v",
				tc.args, gotID, gotJSON, gotOK, tc.wantID, tc.wantJSON, tc.wantOK)
		}
	}
}

func TestOriginReceiptSkipsHybridV1AndPrefersReverifiedV2(t *testing.T) {
	db := openLocalDB(t, t.TempDir())
	defer db.Close()
	const sampleID = "sha256:upgrade"

	oldHybrid := domain.VerificationReceipt{
		SchemaVersion:    1,
		SampleID:         sampleID,
		Stages:           map[string]string{"contract": sandbox.ResultPass},
		ResolvedPackages: []string{"pkg:npm/axios@1.12.4"},
		CreatedAt:        "2026-08-17T00:00:00Z",
	}
	if err := db.SaveReceipt(context.Background(), oldHybrid); err != nil {
		t.Fatal(err)
	}
	reverified := oldHybrid
	reverified.SchemaVersion = 2
	reverified.CreatedAt = "2026-08-17T00:01:00Z"
	if err := db.SaveReceipt(context.Background(), reverified); err != nil {
		t.Fatal(err)
	}

	got, ok := originReceipt(context.Background(), &sampleEnv{db: db}, sampleID)
	if !ok || got.SchemaVersion != 2 {
		t.Fatalf("origin = schema v%d, ok=%v; want reverified v2", got.SchemaVersion, ok)
	}
}

func TestOriginReceiptFallsBackToValidLegacyV1(t *testing.T) {
	db := openLocalDB(t, t.TempDir())
	defer db.Close()
	const sampleID = "sha256:legacy"
	r := domain.VerificationReceipt{
		SchemaVersion: 1,
		SampleID:      sampleID,
		Stages:        map[string]string{"contract": sandbox.ResultPass},
		CreatedAt:     "2026-08-17T00:00:00Z",
	}
	if err := db.SaveReceipt(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	got, ok := originReceipt(context.Background(), &sampleEnv{db: db}, sampleID)
	if !ok || got.SchemaVersion != 1 {
		t.Fatalf("origin = schema v%d, ok=%v; want legacy v1", got.SchemaVersion, ok)
	}
}

func TestSampleListShowsSamples(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	sampleID := createLocalSample(t, home, nil)

	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "list"}); code != 0 {
		t.Fatalf("list exited %d\nstderr: %s", code, errBuf)
	}
	got := out.String()
	short := strings.TrimPrefix(sampleID, "sha256:")[:12]
	for _, want := range []string{short, "LOCAL", "MIT-0", "post a JSON body with axios"} {
		if !strings.Contains(got, want) {
			t.Fatalf("list output missing %q:\n%s", want, got)
		}
	}
}

func TestSamplePublishHappyPathMultipartAndBearer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	bypassPublishApproval(t)
	sampleID := createLocalSample(t, home, nil)

	// Configure an API token + login so the Bearer header is attached.
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.GithubLogin = "alice"
	cfg.APIToken = "test-token-123"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}

	rec := &publishRecord{}
	srv := newPublishServer(t, rec)

	out, errBuf := captureSampleIO(t, "")
	code := Main([]string{"sample", "publish", sampleID, "--server", srv.URL})
	if code != 0 {
		t.Fatalf("publish exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	if rec.calls != 1 {
		t.Fatalf("server calls = %d, want 1", rec.calls)
	}
	if rec.auth != "Bearer test-token-123" {
		t.Fatalf("Authorization = %q, want Bearer test-token-123", rec.auth)
	}
	if rec.sampleID != sampleID {
		t.Fatalf("uploaded sampleId = %q, want %q", rec.sampleID, sampleID)
	}
	if rec.manifest == "" || !strings.Contains(rec.manifest, "pkg:npm/axios@1.12.0") {
		t.Fatalf("uploaded manifest field wrong: %q", rec.manifest)
	}
	if !rec.artifactOK || rec.artifactLen == 0 {
		t.Fatalf("uploaded artifact does not hash to the claimed sampleId (len %d)", rec.artifactLen)
	}
	if !strings.Contains(out.String(), srv.URL+"/samples/"+sampleID) {
		t.Fatalf("public URL not printed:\n%s", out.String())
	}

	db := openLocalDB(t, home)
	defer db.Close()
	row, ok, err := db.GetSample(context.Background(), sampleID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if row.Status != "PUBLISHED" {
		t.Fatalf("local status = %q, want PUBLISHED", row.Status)
	}
}

func TestSamplePublishAnonymousOmitsBearer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	bypassPublishApproval(t)
	sampleID := createLocalSample(t, home, nil)

	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIToken = "test-token-123"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}

	rec := &publishRecord{}
	srv := newPublishServer(t, rec)
	out, errBuf := captureSampleIO(t, "")
	code := Main([]string{"sample", "publish", sampleID, "--anonymous", "--server", srv.URL})
	if code != 0 {
		t.Fatalf("publish exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	if rec.auth != "" {
		t.Fatalf("anonymous publish sent Authorization %q, want none", rec.auth)
	}
}

func TestSamplePublishTypedYesGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	sampleID := createLocalSample(t, home, nil)

	rec := &publishRecord{}
	srv := newPublishServer(t, rec)

	// Wrong answer → abort, nothing uploaded.
	out, errBuf := captureSampleIO(t, "no\n")
	if code := Main([]string{"sample", "publish", sampleID, "--server", srv.URL}); code == 0 {
		t.Fatalf("publish with answer \"no\" exited 0\nstdout: %s", out)
	}
	if rec.calls != 0 {
		t.Fatalf("server was called %d times after refusal, want 0", rec.calls)
	}
	if !strings.Contains(errBuf.String(), "aborted") {
		t.Fatalf("expected abort message, got:\n%s", errBuf)
	}

	// A process environment value cannot open a production approval bypass.
	t.Setenv("CSX_TEST_ASSUME_YES", "1")
	out, _ = captureSampleIO(t, "nah\n")
	if code := Main([]string{"sample", "publish", sampleID, "--server", srv.URL}); code == 0 {
		t.Fatal("CSX_TEST_ASSUME_YES skipped the typed-yes gate")
	}
	if rec.calls != 0 {
		t.Fatalf("server was called %d times, want 0", rec.calls)
	}
	if !strings.Contains(out.String(), "[PUBLISH]") {
		t.Fatalf("approval prompt missing:\n%s", out)
	}

	// Typed "yes" → publishes.
	out, errBuf = captureSampleIO(t, "yes\n")
	if code := Main([]string{"sample", "publish", sampleID, "--server", srv.URL}); code != 0 {
		t.Fatalf("publish with typed yes exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	if rec.calls != 1 {
		t.Fatalf("server calls = %d, want 1", rec.calls)
	}
	// Approval screen shows file list, license, and seeder before the prompt.
	got := out.String()
	for _, want := range []string{"index.mjs", "MIT-0", "Seeder:", "anonymous", "[PUBLISH]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("approval screen missing %q:\n%s", want, got)
		}
	}
}

func TestSamplePublishRefusesOnLeakageFindings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	bypassPublishApproval(t)
	// Plant a GitHub token — create succeeds (findings warn), publish must refuse.
	token := "ghp_" + strings.Repeat("a1B2", 9) // 36 chars after prefix
	sampleID := createLocalSample(t, home, map[string]string{
		"notes.md": "auth uses " + token + " for the demo\n",
	})

	rec := &publishRecord{}
	srv := newPublishServer(t, rec)
	out, errBuf := captureSampleIO(t, "")
	code := Main([]string{"sample", "publish", sampleID, "--server", srv.URL})
	if code == 0 {
		t.Fatalf("publish with a planted token exited 0\nstdout: %s", out)
	}
	if rec.calls != 0 {
		t.Fatalf("server was called %d times, want 0 (refusal must happen before upload)", rec.calls)
	}
	got := errBuf.String()
	for _, want := range []string{"REFUSED", samples.KindGitHubToken, "notes.md", "no override"} {
		if !strings.Contains(got, want) {
			t.Fatalf("refusal output missing %q:\n%s", want, got)
		}
	}
}

func TestSamplePublishRejectsHashMismatchBeforeUpload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	bypassPublishApproval(t)
	sampleID := createLocalSample(t, home, nil)

	// Tamper with the cached artifact so its content no longer hashes to
	// the claimed sampleId.
	hexID := strings.TrimPrefix(sampleID, "sha256:")
	objPath := filepath.Join(home, "cas", "sha256", hexID[0:2], hexID[2:4], hexID)
	if err := os.WriteFile(objPath, []byte("tampered bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &publishRecord{}
	srv := newPublishServer(t, rec)
	out, errBuf := captureSampleIO(t, "")
	code := Main([]string{"sample", "publish", sampleID, "--server", srv.URL})
	if code == 0 {
		t.Fatalf("publish of tampered artifact exited 0\nstdout: %s", out)
	}
	if rec.calls != 0 {
		t.Fatalf("server was called %d times, want 0 (mismatch must be rejected client-side)", rec.calls)
	}
	if !strings.Contains(errBuf.String(), "mismatch") {
		t.Fatalf("expected hash mismatch refusal, got:\n%s", errBuf)
	}
}

func TestSampleCreateDefaultsLicenseAndCapsContractless(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	// Build a manifest with no license and no contract command.
	dir := t.TempDir()
	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1,
			Kind:          "HOW",
			Goal:          "read a TOML file",
			Packages:      []string{"pkg:cargo/toml@0.8.0"},
			Contract:      []string{"parses the file"},
		},
		Packages:        []string{"pkg:cargo/toml@0.8.0"},
		Environment:     domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "cargo", OS: "linux", Arch: "x64"},
		VerifierAdapter: "node-typescript@1",
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "csx.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.rs"), []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "create", dir}); code != 0 {
		t.Fatalf("create exited %d\nstderr: %s", code, errBuf)
	}
	got := out.String()
	if !strings.Contains(got, "MIT-0") {
		t.Fatalf("default license MIT-0 not applied/printed:\n%s", got)
	}
	if !strings.Contains(got, string(domain.L2Compiled)) {
		t.Fatalf("contract-less sample must print the %s cap note:\n%s", domain.L2Compiled, got)
	}
}

// Proposals are saved with the absolute clean-room workspace path. When
// a user runs `csx sample create .` from inside that directory (or with a
// relative path), sample creation must resolve the path so SetProposalState
// updates the stored row. Otherwise, the proposal remains 'pending' and
// `csx sample pending` continues to offer already-created work indefinitely.
func TestSampleCreateFromRelativePathClearsPendingProposal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	dir := sampleFixtureDir(t, nil)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}

	db := openLocalDB(t, home)
	err = db.SaveProposal(context.Background(), localdb.ProposalRow{
		Workdir:  absDir,
		Goal:     "post a JSON body with axios",
		Packages: []string{"pkg:npm/axios@1.12.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	t.Chdir(absDir)
	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "create", "."}); code != 0 {
		t.Fatalf("sample create . exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}

	db = openLocalDB(t, home)
	defer db.Close()
	pending, err := db.PendingProposals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending proposals after create, got %d (workdir: %s, state: %s)",
			len(pending), pending[0].Workdir, pending[0].State)
	}

	out, errBuf = captureSampleIO(t, "")
	if code := Main([]string{"sample", "pending"}); code != 0 {
		t.Fatalf("sample pending exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	if !strings.Contains(out.String(), "Nothing pending") {
		t.Fatalf("expected 'Nothing pending', got:\n%s", out.String())
	}
}
