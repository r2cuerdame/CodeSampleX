package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func saveSampleForVerification(t *testing.T, store *serverstore.Fake, suffix string) string {
	t.Helper()
	sampleID := "sha256:" + strings.Repeat(suffix, 32)
	saveSampleWithID(t, store, sampleID)
	return sampleID
}

func saveSampleWithID(t *testing.T, store *serverstore.Fake, sampleID string) {
	t.Helper()
	manifest := testManifest()
	if err := store.SaveSample(context.Background(), serverstore.SampleRow{
		SampleID:     sampleID,
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status:       "PUBLISHED", License: "MIT-0", SizeBytes: 512, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
}

type verifyResponse struct {
	Status       string `json:"status"`
	SampleStatus string `json:"sampleStatus"`
}

func TestVerificationCrossPassTransition(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "1a")

	origin, _ := newPeer(t)
	other, _ := newPeer(t)
	env := nodeEnv("esm") // same os → CROSS_PASS but not MATRIX

	// Origin's own PASS does not cross-verify.
	var out verifyResponse
	resp := postJSON(t, srv.URL+"/v1/verifications",
		signedReceipt(t, origin, sampleID, env, "PASS"), &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out.Status != "accepted" || out.SampleStatus != "PUBLISHED" {
		t.Fatalf("after origin receipt: %+v, want PUBLISHED", out)
	}

	// A second, different peer with contract PASS flips to CROSS_PASS.
	resp = postJSON(t, srv.URL+"/v1/verifications",
		signedReceipt(t, other, sampleID, env, "PASS"), &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out.SampleStatus != "CROSS_PASS" {
		t.Fatalf("sampleStatus = %s, want CROSS_PASS", out.SampleStatus)
	}
	sample, _, _ := store.GetSample(context.Background(), sampleID)
	if sample.Status != "CROSS_PASS" {
		t.Fatalf("stored status = %s", sample.Status)
	}
}

func TestVerificationMatrixAndStableTransitions(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "2b")

	origin, _ := newPeer(t)
	peerB, _ := newPeer(t)
	peerC, _ := newPeer(t)

	envWin := nodeEnv("esm")
	envLinux := nodeEnv("esm")
	envLinux.OS = "linux"
	envMac := nodeEnv("esm")
	envMac.OS = "darwin"

	var out verifyResponse
	postJSON(t, srv.URL+"/v1/verifications", signedReceipt(t, origin, sampleID, envWin, "PASS"), &out)
	if out.SampleStatus != "PUBLISHED" {
		t.Fatalf("after origin: %s", out.SampleStatus)
	}

	// Cross-peer PASS on a different os boundary ⇒ MATRIX_PASS directly
	// (cross condition + ≥2 distinct os values).
	postJSON(t, srv.URL+"/v1/verifications", signedReceipt(t, peerB, sampleID, envLinux, "PASS"), &out)
	if out.SampleStatus != "MATRIX_PASS" {
		t.Fatalf("after cross-boundary receipt: %s, want MATRIX_PASS", out.SampleStatus)
	}

	// Third distinct passing peer, no FAIL in 30d ⇒ STABLE.
	postJSON(t, srv.URL+"/v1/verifications", signedReceipt(t, peerC, sampleID, envMac, "PASS"), &out)
	if out.SampleStatus != "STABLE" {
		t.Fatalf("after third peer: %s, want STABLE", out.SampleStatus)
	}
}

func TestVerificationRecentFailBlocksStable(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "3c")

	origin, _ := newPeer(t)
	peerB, _ := newPeer(t)
	peerC, _ := newPeer(t)
	peerD, _ := newPeer(t)

	envWin := nodeEnv("esm")
	envLinux := nodeEnv("esm")
	envLinux.OS = "linux"

	var out verifyResponse
	postJSON(t, srv.URL+"/v1/verifications", signedReceipt(t, origin, sampleID, envWin, "PASS"), &out)
	postJSON(t, srv.URL+"/v1/verifications", signedReceipt(t, peerB, sampleID, envLinux, "PASS"), &out)
	// A recent FAIL from another peer.
	postJSON(t, srv.URL+"/v1/verifications", signedReceipt(t, peerD, sampleID, envWin, "FAIL"), &out)
	// Third passing peer — but the recent FAIL blocks STABLE.
	postJSON(t, srv.URL+"/v1/verifications", signedReceipt(t, peerC, sampleID, envWin, "PASS"), &out)
	if out.SampleStatus != "MATRIX_PASS" {
		t.Fatalf("sampleStatus = %s, want MATRIX_PASS (FAIL in 30d blocks STABLE)", out.SampleStatus)
	}
}

func TestVerificationRejectsTamperedSignature(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "4d")

	priv, _ := newPeer(t)
	receipt := signedReceipt(t, priv, sampleID, nodeEnv("esm"), "PASS")
	receipt.Stages["contract"] = "FAIL" // tampered after signing

	resp := postJSON(t, srv.URL+"/v1/verifications", receipt, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invalid receipt signature") {
		t.Fatalf("body = %s", body)
	}
	// Nothing was stored.
	receipts, _ := store.ReceiptsForSample(context.Background(), sampleID)
	if len(receipts) != 0 {
		t.Fatalf("receipts = %d, want 0", len(receipts))
	}
}

func TestVerificationRejectsPeerIDMismatch(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "5e")

	priv, _ := newPeer(t)
	pub := priv.Public()
	_ = pub
	receipt := signedReceipt(t, priv, sampleID, nodeEnv("esm"), "PASS")
	// Re-sign with a lying peerId: the signature itself is valid, but the
	// fingerprint does not match the embedded pubkey.
	receipt.PeerID = "ed25519:" + strings.Repeat("0", 16)
	receipt.PeerSignature = base64.StdEncoding.EncodeToString(
		signWith(t, priv, receipt))

	resp := postJSON(t, srv.URL+"/v1/verifications", receipt, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "does not match peerPubkey") {
		t.Fatalf("body = %s", body)
	}
}

func TestVerificationUnknownSample(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	priv, _ := newPeer(t)
	receipt := signedReceipt(t, priv, "sha256:"+strings.Repeat("99", 32), nodeEnv("esm"), "PASS")
	resp := postJSON(t, srv.URL+"/v1/verifications", receipt, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// --- verification jobs ---------------------------------------------------------

func TestJobsListAndClaim(t *testing.T) {
	var sampleID string
	srv, store, _ := newTestServer(t, func(d *Deps) {
		var err error
		sampleID, err = d.Blobs.Put(t.Context(), bytes.NewBufferString("job artifact"))
		if err != nil {
			t.Fatal(err)
		}
	})
	saveSampleWithID(t, store, sampleID)
	ctx := context.Background()
	id, err := store.CreateJob(ctx, serverstore.JobRow{SampleID: sampleID, Reason: "cross", Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateJob(ctx, serverstore.JobRow{
		SampleID: sampleID, Reason: "matrix", Status: "open",
		WantEnvJSON: `{"sandboxCapability":"CONTAINER_RUN"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	var list struct {
		Jobs []struct {
			ID       int64  `json:"id"`
			SampleID string `json:"sampleId"`
			Reason   string `json:"reason"`
		} `json:"jobs"`
	}
	getJSON(t, srv.URL+"/v1/verification/jobs?limit=10", &list)
	if len(list.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(list.Jobs))
	}

	// Capability filter: COMPILE_ONLY peers must not see the pinned job.
	getJSON(t, srv.URL+"/v1/verification/jobs?capability=COMPILE_ONLY", &list)
	if len(list.Jobs) != 1 || list.Jobs[0].Reason != "cross" {
		t.Fatalf("filtered jobs = %+v", list.Jobs)
	}

	_, peerID := newPeer(t)
	var claim struct {
		Status string `json:"status"`
	}
	resp := postJSON(t, srv.URL+"/v1/verification/jobs/1/claim",
		map[string]string{"peerId": peerID}, &claim)
	if resp.StatusCode != http.StatusOK || claim.Status != "claimed" {
		t.Fatalf("claim status = %d %+v", resp.StatusCode, claim)
	}
	_ = id

	// Second claim conflicts.
	resp = postJSON(t, srv.URL+"/v1/verification/jobs/1/claim",
		map[string]string{"peerId": peerID}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second claim status = %d, want 409", resp.StatusCode)
	}

	// Bad peer id.
	resp = postJSON(t, srv.URL+"/v1/verification/jobs/2/claim",
		map[string]string{"peerId": "not-a-peer"}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad peer claim status = %d, want 400", resp.StatusCode)
	}
}

func TestVerificationReceiptCompletesOnlyItsExactClaimedMatrixJob(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := "sha256:" + strings.Repeat("7a", 32)
	manifest := testManifest()
	manifest.Packages = []string{"pkg:maven/org.example/library@1.0.0"}
	manifest.Environment = domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "maven", Runtime: "java", RuntimeVersion: "21",
		Language: "java", LanguageVersion: "17", ExecutionContext: "java",
	}
	manifest.VerifierAdapter = "maven-java@1"
	if err := store.SaveSample(t.Context(), serverstore.SampleRow{
		SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status: "PUBLISHED", License: "MIT-0", CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	priv, peerID := newPeer(t)
	want := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "maven-java@1",
		Ecosystem: "maven", Runtime: "java", RuntimeVersion: "17", ExecutionContext: "java",
	}
	jobID, err := store.CreateJob(t.Context(), serverstore.JobRow{
		SampleID: sampleID, Reason: "matrix", Status: "open",
		WantEnvJSON: string(domain.MustCanonicalJSON(want)),
	})
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := store.CreateJob(t.Context(), serverstore.JobRow{
		SampleID: sampleID, Reason: "matrix", Status: "open",
		WantEnvJSON: `{"sandboxCapability":"CONTAINER_RUN","verifierAdapter":"maven-java@1","ecosystem":"maven","runtime":"java","runtimeVersion":"21","executionContext":"java"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ClaimJob(t.Context(), jobID, peerID); err != nil || !ok {
		t.Fatalf("claim exact job: ok=%v err=%v", ok, err)
	}
	if ok, err := store.ClaimJob(t.Context(), otherID, peerID); err != nil || ok {
		t.Fatalf("same peer simultaneously claimed a second matrix for the sample: ok=%v err=%v", ok, err)
	}

	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "maven", OS: "linux", Arch: "amd64",
		Runtime: "java", RuntimeVersion: "21", Language: "java", LanguageVersion: "17",
		ExecutionContext: "java", Virtualization: "container", ContainerRuntime: "docker",
		Distro: "amzn", OSVersionBucket: "2023", Libc: "glibc",
	}
	receipt := signedReceipt(t, priv, sampleID, env, "PASS")
	receipt.VerifierAdapter = "maven-java@1"
	receipt.PeerSignature = base64.StdEncoding.EncodeToString(signWith(t, priv, receipt))
	post := func(rec domain.VerificationReceipt) *http.Response {
		body, marshalErr := json.Marshal(rec)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		req, requestErr := http.NewRequest(http.MethodPost, srv.URL+"/v1/verifications", bytes.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(domain.VerificationJobIDHeader, strconv.FormatInt(jobID, 10))
		resp, requestErr := srv.Client().Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	if resp := post(receipt); resp.StatusCode != http.StatusConflict {
		t.Fatalf("wrong JDK receipt status = %d, want 409", resp.StatusCode)
	}
	if rows, _ := store.ReceiptsForSample(t.Context(), sampleID); len(rows) != 0 {
		t.Fatal("mismatched matrix receipt was persisted")
	}

	receipt.Environment.RuntimeVersion = "17"
	receipt.EnvironmentHash = receipt.Environment.Hash()
	receipt.PeerSignature = base64.StdEncoding.EncodeToString(signWith(t, priv, receipt))
	if resp := post(receipt); resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("exact matrix receipt status = %d body=%s", resp.StatusCode, body)
	}
	job, _, _ := store.Job(t.Context(), jobID)
	other, _, _ := store.Job(t.Context(), otherID)
	if job.Status != "done" || other.Status != "open" {
		t.Fatalf("job states exact=%s other=%s, want done/open", job.Status, other.Status)
	}
}

// TestUnknownEnvironmentIsNotDiversity pins the L5 gate. Receipts written
// before the runner stamped the sandbox environment claim the HOST os with
// every other field blank — 103 of them on the live network said "windows"
// for contracts that had executed in a linux container. Counting an
// environment nobody described as a distinct one is what granted every
// MATRIX_PASS there, and it is the same hole a Sybil would use: minting
// environments costs nothing if they are allowed to be blank.
func TestUnknownEnvironmentIsNotDiversity(t *testing.T) {
	full := func(os, runtime, version string) compatibility.ReceiptInfo {
		return compatibility.ReceiptInfo{Env: domain.EnvironmentFingerprint{
			SchemaVersion: 1, OS: os, Runtime: runtime, RuntimeVersion: version,
		}}
	}
	// The exact shape of the bad receipts: an OS name and nothing else.
	blank := compatibility.ReceiptInfo{Env: domain.EnvironmentFingerprint{
		SchemaVersion: 1, OS: "windows",
	}}

	if spansContextBoundary([]compatibility.ReceiptInfo{blank, full("linux", "node", "22")}) {
		t.Error("an undescribed environment must not count as a second one")
	}
	if spansContextBoundary([]compatibility.ReceiptInfo{blank, blank}) {
		t.Error("two undescribed environments are not diversity")
	}
	// Real boundaries still qualify.
	if !spansContextBoundary([]compatibility.ReceiptInfo{
		full("linux", "node", "22"), full("windows", "node", "22")}) {
		t.Error("two real operating systems should span a boundary")
	}
	if !spansContextBoundary([]compatibility.ReceiptInfo{
		full("linux", "node", "22"), full("linux", "node", "24")}) {
		t.Error("two real runtime majors should span a boundary")
	}
	if spansContextBoundary([]compatibility.ReceiptInfo{
		full("linux", "node", "22"), full("linux", "node", "22")}) {
		t.Error("the same environment twice is not diversity")
	}
}

// TestRecomputeStatusCanDowngrade: the live path only upgrades, which is
// right while the rules hold still. When a rule is corrected, a status
// earned under the old one is wrong, and continuing to advertise it claims
// verification the evidence does not support.
func TestRecomputeStatusCanDowngrade(t *testing.T) {
	now := testNow
	origin := "ed25519:1111111111111111"
	other := "ed25519:2222222222222222"

	receipt := func(peer, os, runtime, version string) serverstore.ReceiptRow {
		r := domain.VerificationReceipt{
			SchemaVersion: 1, SampleID: "sha256:x", PeerID: peer,
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, OS: os, Runtime: runtime, RuntimeVersion: version,
			},
			Stages: map[string]string{"contract": "PASS"},
		}
		return serverstore.ReceiptRow{
			SampleID: "sha256:x", PeerID: peer, ContractResult: "PASS",
			ReceiptJSON: string(domain.MustCanonicalJSON(r)), CreatedAt: now,
		}
	}

	// Exactly the live data: one real container receipt, one that names an
	// OS and describes nothing else.
	rows := []serverstore.ReceiptRow{
		receipt(origin, "windows", "", ""),
		receipt(other, "linux", "node", "22"),
	}
	got := RecomputeStatus(rows, now)
	if got != "CROSS_PASS" {
		t.Fatalf("status = %q, want CROSS_PASS: a second peer reproduced it, but no real boundary was crossed", got)
	}

	// A genuine boundary still reaches MATRIX_PASS.
	rows = append(rows, receipt(other, "windows", "node", "22"))
	if got := RecomputeStatus(rows, now); got != "MATRIX_PASS" {
		t.Fatalf("status = %q, want MATRIX_PASS once two described environments differ", got)
	}
}
