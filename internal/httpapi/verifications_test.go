package httpapi

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func saveSampleForVerification(t *testing.T, store *serverstore.Fake, suffix string) string {
	t.Helper()
	manifest := testManifest()
	sampleID := "sha256:" + strings.Repeat(suffix, 32)
	if err := store.SaveSample(context.Background(), serverstore.SampleRow{
		SampleID:     sampleID,
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status:       "PUBLISHED", License: "MIT-0", SizeBytes: 512, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	return sampleID
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
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "6f")
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
