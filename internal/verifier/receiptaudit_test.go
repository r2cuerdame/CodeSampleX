package verifier

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

// signedRow re-signs a receipt after a test has changed it and returns the
// stored row that a truthful store would hold for it.
func signedRow(t *testing.T, ident *identity.Identity, r domain.VerificationReceipt) AuditedReceipt {
	t.Helper()
	r.PeerSignature = ""
	r.PeerSignature = ident.Sign(r.SigningBytes())
	return AuditedReceipt{
		ReceiptID: r.ReceiptID(),
		PeerID:    r.PeerID,
		SampleID:  r.SampleID,
		EnvHash:   r.EnvironmentHash,
		CreatedAt: r.CreatedAt,
		Receipt:   r,
		// A truthful store holds the document, not only the decode of it.
		// The round-trip check compares the two, so a row without the stored
		// bytes is a row that check cannot speak about.
		RawReceipt: domain.MustCanonicalJSON(r),
	}
}

func auditFixture(t *testing.T) (*identity.Identity, domain.VerificationReceipt) {
	t.Helper()
	ident := testIdentity(t)
	r, err := Run(context.Background(), allPassRunner(), domain.CapContainerRun,
		fixtureDir(t), testManifest(), ident, testEnv())
	if err != nil {
		t.Fatal(err)
	}
	return ident, r
}

// publishedImage is a real entry of the verifier registry. Telling this apart
// from a reference that merely LOOKS pinned is the audit's job, so the test
// may not invent the digest: it has to come from the registry itself.
func publishedImage(t *testing.T) domain.VerifierImage {
	t.Helper()
	refs := sandbox.PublishedReferences()
	if len(refs) == 0 {
		t.Fatal("the verifier registry published nothing")
	}
	img, ok := sandbox.PublishedImage(refs[0])
	if !ok {
		t.Fatalf("the registry does not recognise its own published reference %q", refs[0])
	}
	return img
}

// R2C-81 asked three times (2026-08-23, 08-24, 08-29) whether production
// receipts name the image the registry publishes. Each time the answer had to
// be recomputed by hand, because a well-formed pin and a PUBLISHED pin are
// different claims and only the shape was checkable from outside the sandbox
// package. node:22-alpine@sha256:aaaa... is syntactically perfect and names
// bytes that do not exist.
func TestAWellFormedPinIsNotYetAPublishedImage(t *testing.T) {
	ident, r := auditFixture(t)
	invented := *r.VerifierImage // the fake runner's node:22-alpine@sha256:aaaa...
	rows := []AuditedReceipt{signedRow(t, ident, r)}

	got := AuditReceipts(rows)
	if got.DigestPinned != 1 {
		t.Errorf("DigestPinned = %d, want 1: %q is a well-formed pin", got.DigestPinned, invented.Reference)
	}
	if got.ReferenceDigestAgree != 1 {
		t.Errorf("ReferenceDigestAgree = %d, want 1", got.ReferenceDigestAgree)
	}
	if got.PublishedImage != 0 {
		t.Errorf("PublishedImage = %d, want 0: %q is not in the registry", got.PublishedImage, invented.Reference)
	}
	if len(got.Problems) == 0 {
		t.Error("an unpublished pin was counted clean and reported nothing")
	}
}

// The published case has to be counted too, or the check above would pass by
// never counting anything.
func TestAPublishedImageIsRecognised(t *testing.T) {
	ident, r := auditFixture(t)
	img := publishedImage(t)
	r.VerifierImage = &img
	rows := []AuditedReceipt{signedRow(t, ident, r)}

	got := AuditReceipts(rows)
	if got.WithImage != 1 || got.PublishedImage != 1 || got.DigestPinned != 1 {
		t.Errorf("withImage=%d published=%d pinned=%d, want 1/1/1 for %q",
			got.WithImage, got.PublishedImage, got.DigestPinned, img.Reference)
	}
	if got.SignatureValid != 1 || got.PeerIDBinding != 1 || got.ReceiptIDMatches != 1 || got.RowBinding != 1 {
		t.Errorf("sig=%d peer=%d id=%d row=%d, want all 1",
			got.SignatureValid, got.PeerIDBinding, got.ReceiptIDMatches, got.RowBinding)
	}
	if len(got.Problems) != 0 {
		t.Errorf("a clean receipt reported problems: %v", got.Problems)
	}
	if got.ByReference[img.Reference] != 1 {
		t.Errorf("ByReference[%q] = %d, want 1", img.Reference, got.ByReference[img.Reference])
	}
}

// A mutable tag is the failure this whole field exists to prevent: two
// workers can sign receipts naming the same tag for runs of different bytes.
func TestAMutableTagIsNotADigestPin(t *testing.T) {
	ident, r := auditFixture(t)
	r.VerifierImage = &domain.VerifierImage{Reference: "node:22-alpine", Digest: ""}
	got := AuditReceipts([]AuditedReceipt{signedRow(t, ident, r)})

	if got.DigestPinned != 0 {
		t.Errorf("DigestPinned = %d, want 0 for a mutable tag", got.DigestPinned)
	}
	if got.PublishedImage != 0 {
		t.Errorf("PublishedImage = %d, want 0 for a mutable tag", got.PublishedImage)
	}
}

// Storage is not the signature. A receipt whose bytes were edited after
// signing must fail on the signature AND on the content hash, and the audit
// has to say so rather than trust the row it was handed.
func TestATamperedReceiptFailsSignatureAndContentHash(t *testing.T) {
	ident, r := auditFixture(t)
	row := signedRow(t, ident, r)

	swapped := *row.Receipt.VerifierImage
	swapped.Digest = "sha256:" + strings.Repeat("b", 64)
	swapped.Reference = strings.SplitN(swapped.Reference, "@", 2)[0] + "@" + swapped.Digest
	row.Receipt.VerifierImage = &swapped // the stored ReceiptID still names the old bytes

	got := AuditReceipts([]AuditedReceipt{row})
	if got.SignatureValid != 0 {
		t.Error("a receipt edited after signing verified against its own signature")
	}
	if got.ReceiptIDMatches != 0 {
		t.Error("the stored receipt id still matched a document that changed")
	}
}

// The peer id is derived from the key, so a receipt may not claim an identity
// that its own public key does not hash to.
func TestAPeerIdThatTheKeyDoesNotHashToIsFlagged(t *testing.T) {
	ident, r := auditFixture(t)
	r.PeerID = "ed25519:0000000000000000"
	got := AuditReceipts([]AuditedReceipt{signedRow(t, ident, r)})

	if got.PeerIDBinding != 0 {
		t.Error("a receipt claiming a peer id its key does not derive was counted as bound")
	}
}

// The store's columns are an index, not a second source of truth. If they
// disagree with the signed document, the document wins and the row is wrong.
func TestARowThatDisagreesWithItsDocumentIsFlagged(t *testing.T) {
	ident, r := auditFixture(t)
	row := signedRow(t, ident, r)
	row.EnvHash = "sha256:" + strings.Repeat("c", 64)

	got := AuditReceipts([]AuditedReceipt{row})
	if got.RowBinding != 0 {
		t.Error("a row whose env_hash column disagrees with the signed document was counted as bound")
	}
	if got.SignatureValid != 1 {
		t.Error("editing a column must not disturb the document's signature")
	}
}

// A receipt with no image is not a receipt with a wrong image. The native
// fallback never entered a container, and pre-v0.1.43 peers could not record
// one; counting either as a pinning failure would manufacture a problem.
func TestAReceiptWithoutAnImageIsNotAPinningFailure(t *testing.T) {
	ident := testIdentity(t)
	r := allPassRunner()
	r.noImage = true
	receipt, err := Run(context.Background(), r, domain.CapCompileOnly,
		fixtureDir(t), testManifest(), ident, testEnv())
	if err != nil {
		t.Fatal(err)
	}
	got := AuditReceipts([]AuditedReceipt{signedRow(t, ident, receipt)})

	if got.Receipts != 1 || got.WithImage != 0 {
		t.Errorf("receipts=%d withImage=%d, want 1/0", got.Receipts, got.WithImage)
	}
	if len(got.Problems) != 0 {
		t.Errorf("an image-less receipt was reported as a problem: %v", got.Problems)
	}
	if got.SignatureValid != 1 {
		t.Errorf("SignatureValid = %d, want 1", got.SignatureValid)
	}
}

// The dump reader exists because COPY ... TO STDOUT escapes backslashes and
// corrupts every JSON string in a manifest, so a production dump has to
// travel base64-encoded, one row per line.
func TestReceiptDumpRoundTripsARow(t *testing.T) {
	ident, r := auditFixture(t)
	row := signedRow(t, ident, r)

	payload, err := json.Marshal(map[string]any{
		"receiptId": row.ReceiptID, "peerId": row.PeerID, "sampleId": row.SampleID,
		"envHash": row.EnvHash, "createdAt": row.CreatedAt, "receipt": row.Receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	line := base64.StdEncoding.EncodeToString(payload)

	rows, err := ReadReceiptDump(strings.NewReader(line + "\n\n" + line + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("read %d rows, want 2 (blank lines are skipped)", len(rows))
	}
	if rows[0].ReceiptID != row.ReceiptID || rows[0].Receipt.ReceiptID() != row.ReceiptID {
		t.Error("the decoded row does not hash back to its stored id")
	}
	if AuditReceipts(rows).SignatureValid != 2 {
		t.Error("a receipt that survived the dump no longer verifies")
	}
}
