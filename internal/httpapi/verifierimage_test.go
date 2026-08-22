package httpapi

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

const testImageDigest = "sha256:c610fcdfb1d5b4740dd70c284ed3cb16bb857e0f7166196e36a5501df7a3aa32"

func v2ContainerReceipt(t *testing.T, priv ed25519.PrivateKey, sampleID string,
	img *domain.VerifierImage) domain.VerificationReceipt {
	t.Helper()
	r := signedReceipt(t, priv, sampleID, containerEnv(), "PASS")
	r.SchemaVersion = 2
	r.VerifierImage = img
	r.PeerSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, r.SigningBytes()))
	return r
}

// A recorded image is only worth having if it is what it says it is. These
// are the claims the server can check from the document alone; each refuses
// a receipt that contradicts itself, so no honest peer can trip them.
func TestServerRefusesAnImageClaimThatIsNotAPin(t *testing.T) {
	for _, tc := range []struct {
		name string
		img  *domain.VerifierImage
	}{
		{"a mutable tag", &domain.VerifierImage{Reference: "node:22-alpine", Digest: testImageDigest}},
		{"a truncated digest", &domain.VerifierImage{
			Reference: "node:22-alpine@sha256:c610fcdf", Digest: "sha256:c610fcdf"}},
		{"a digest that is not the reference's", &domain.VerifierImage{
			Reference: "node:22-alpine@" + testImageDigest,
			Digest:    "sha256:" + strings.Repeat("b", 64)}},
		{"nothing at all", &domain.VerifierImage{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, _ := newTestServer(t, nil)
			sampleID := saveSampleForVerification(t, store, "7d")
			priv, _ := newPeer(t)

			var out verifyResponse
			resp := postJSON(t, srv.URL+"/v1/verifications",
				v2ContainerReceipt(t, priv, sampleID, tc.img), &out)
			if resp.StatusCode != 400 {
				t.Fatalf("status = %d, want 400 for %s", resp.StatusCode, tc.name)
			}
			rows, err := store.ReceiptsForSample(t.Context(), sampleID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Errorf("the refused receipt was stored anyway: %d rows", len(rows))
			}
		})
	}
}

// The field is new, and the peers already in the field are not. A receipt
// that omits it says NOTHING about which bytes ran — which is the truth for
// every receipt signed before this existed — and must still be accepted.
func TestAReceiptWithoutAnImageIsStillAccepted(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "7e")
	priv, _ := newPeer(t)

	var out verifyResponse
	resp := postJSON(t, srv.URL+"/v1/verifications", v2ContainerReceipt(t, priv, sampleID, nil), &out)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 for a receipt from a peer that predates verifierImage", resp.StatusCode)
	}
	rows, err := store.ReceiptsForSample(t.Context(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored %d receipts, want 1", len(rows))
	}
	if strings.Contains(rows[0].ReceiptJSON, "verifierImage") {
		t.Error("an absent image was materialised into the stored receipt")
	}
}

// A pinned image is stored verbatim, so the server, the snapshot and the
// sample page can all say which bytes produced the result.
func TestAPinnedImageSurvivesIntoTheStoredReceipt(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "7f")
	priv, _ := newPeer(t)

	img := &domain.VerifierImage{Reference: "node:22-alpine@" + testImageDigest, Digest: testImageDigest}
	var out verifyResponse
	resp := postJSON(t, srv.URL+"/v1/verifications", v2ContainerReceipt(t, priv, sampleID, img), &out)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	rows, err := store.ReceiptsForSample(t.Context(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored %d receipts, want 1", len(rows))
	}
	if !strings.Contains(rows[0].ReceiptJSON, testImageDigest) {
		t.Errorf("the stored receipt does not carry the image digest: %s", rows[0].ReceiptJSON)
	}
}

// The v1/v2 boundary stays strict. A document must not claim the older
// schema while carrying a field that schema does not define, or the version
// stops meaning anything about what the document can contain.
func TestAV1ReceiptMayNotCarryAnImage(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "80")
	priv, _ := newPeer(t)

	r := signedReceipt(t, priv, sampleID, containerEnv(), "PASS")
	r.VerifierImage = &domain.VerifierImage{
		Reference: "node:22-alpine@" + testImageDigest, Digest: testImageDigest}
	r.PeerSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, r.SigningBytes()))

	var out verifyResponse
	resp := postJSON(t, srv.URL+"/v1/verifications", r, &out)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 for a v1 receipt carrying verifierImage", resp.StatusCode)
	}
	rows, err := store.ReceiptsForSample(t.Context(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("the refused receipt was stored anyway: %d rows", len(rows))
	}
}
