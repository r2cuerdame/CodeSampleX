package verifier

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

// The receipt named the environment — linux, musl, node 22 — but not which
// image produced it. A floating tag can put different bytes behind that
// label on two workers, or on one worker at two times, so "the contract ran
// in a pinned container" was a claim nothing in the receipt could support.
func TestReceiptRecordsTheImageTheStagesRanIn(t *testing.T) {
	dir := fixtureDir(t)
	ident := testIdentity(t)

	receipt, err := Run(context.Background(), allPassRunner(), domain.CapContainerRun,
		dir, testManifest(), ident, testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.VerifierImage == nil {
		t.Fatal("a container verification produced a receipt that does not say which image ran")
	}
	alias, digest, ok := strings.Cut(receipt.VerifierImage.Reference, "@")
	if !ok || alias == "" {
		t.Fatalf("reference %q is not <alias>@<digest>", receipt.VerifierImage.Reference)
	}
	if receipt.VerifierImage.Digest != digest {
		t.Errorf("digest %q disagrees with the reference %q",
			receipt.VerifierImage.Digest, receipt.VerifierImage.Reference)
	}
}

// The image is evidence, so it has to be signed like the rest of the
// receipt. If it sat outside the signature it could be attached, changed or
// stripped after the fact by anyone relaying the document.
func TestTheSignatureCoversTheImage(t *testing.T) {
	dir := fixtureDir(t)
	ident := testIdentity(t)

	receipt, err := Run(context.Background(), allPassRunner(), domain.CapContainerRun,
		dir, testManifest(), ident, testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !identity.Verify(receipt.PeerPubkey, receipt.PeerSignature, receipt.SigningBytes()) {
		t.Fatal("the receipt does not verify against its own signature")
	}

	tampered := receipt
	swapped := *receipt.VerifierImage
	swapped.Digest = "sha256:" + strings.Repeat("b", 64)
	swapped.Reference = strings.SplitN(swapped.Reference, "@", 2)[0] + "@" + swapped.Digest
	tampered.VerifierImage = &swapped
	if identity.Verify(tampered.PeerPubkey, tampered.PeerSignature, tampered.SigningBytes()) {
		t.Error("the image could be swapped without breaking the signature")
	}
	if tampered.ReceiptID() == receipt.ReceiptID() {
		t.Error("two receipts naming different image bytes share a receipt id")
	}
}

// A run that never entered a container must not name one. The native
// fallback resolves and compiles on the host, so there is no image, and an
// absent field is the honest answer rather than the default image.
func TestAHostRunNamesNoImage(t *testing.T) {
	dir := fixtureDir(t)

	r := allPassRunner()
	r.noImage = true
	receipt, err := Run(context.Background(), r, domain.CapCompileOnly,
		dir, testManifest(), testIdentity(t), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.VerifierImage != nil {
		t.Errorf("a host run claimed image %+v", receipt.VerifierImage)
	}
	var wire map[string]any
	if err := json.Unmarshal(domain.MustCanonicalJSON(receipt), &wire); err != nil {
		t.Fatal(err)
	}
	if _, present := wire["verifierImage"]; present {
		// omitempty matters here: a present-but-empty object would be a
		// claim about bytes, and every receipt signed before this field
		// existed must still hash to exactly what it hashed to then.
		t.Error("an unestablished image was written to the wire as an empty claim")
	}
}

// The real container runner has to produce the same thing the engine
// records, keyed off the same selection the stages use. A field derived
// from a second table would eventually describe an image the stages had
// stopped running.
func TestTheDockerRunnerReportsWhatItWillRun(t *testing.T) {
	m := domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", Runtime: "node",
	}}
	img := sandbox.DockerRunner{}.VerifierImage(m)
	if img == nil {
		t.Fatal("the container runner reports no image for an npm sample")
	}
	if !strings.Contains(img.Reference, "@sha256:") || img.Digest == "" {
		t.Fatalf("container runner image is not digest-pinned: %+v", img)
	}
	// An ecosystem with no image must report none rather than a default.
	none := sandbox.DockerRunner{}.VerifierImage(domain.SampleManifest{
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "nuget"},
	})
	if none != nil {
		t.Errorf("an unsupported ecosystem was given image %+v", none)
	}
}
