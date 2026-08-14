package httpapi

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func containerEnv() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm",
		OS: "linux", OSVersionBucket: "alpine", Arch: "x64",
		Runtime: "node", RuntimeVersion: "22.18.1", ModuleSystem: "esm",
		Virtualization: "container", ContainerRuntime: "docker", Libc: "musl",
	}.Normalize()
}

// A peer running a build from before the sandbox rewrote the stage
// environment signed 271 receipts that said CONTAINER_RUN and then
// described the Windows host that launched docker. The signatures were all
// valid; the statements were not. 39 contract failures produced inside a
// container were filed as Windows results, and 39 sample pages showed a
// user a FAIL on an environment where the sample had never been tried.
func TestReceiptCannotClaimAContainerAndDescribeItsHost(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "7c")
	priv, _ := newPeer(t)

	host := containerEnv()
	host.OS, host.OSVersionBucket = "windows", "11"
	host.Virtualization, host.ContainerRuntime, host.Libc = "", "", ""
	host = host.Normalize()

	r := signedReceipt(t, priv, sampleID, host, "FAIL")
	r.SandboxCapability = domain.CapContainerRun
	r.PeerSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, r.SigningBytes()))

	var out verifyResponse
	resp := postJSON(t, srv.URL+"/v1/verifications", r, &out)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 for a container receipt describing its host", resp.StatusCode)
	}

	rows, err := store.ReceiptsForSample(t.Context(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("the refused receipt was stored anyway: %d rows", len(rows))
	}
}

// The graph groups results by environmentHash. A hash that does not belong
// to the attached environment files a result under someone else's bucket —
// the same corruption, reached by a different route.
func TestReceiptHashMustDescribeItsOwnEnvironment(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "7d")
	priv, _ := newPeer(t)

	r := signedReceipt(t, priv, sampleID, containerEnv(), "PASS")
	r.EnvironmentHash = "sha256:" + strings.Repeat("ab", 32)
	r.PeerSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, r.SigningBytes()))

	var out verifyResponse
	if resp := postJSON(t, srv.URL+"/v1/verifications", r, &out); resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 for a mismatched environmentHash", resp.StatusCode)
	}
}

// The gate must not cost an honest peer anything: the receipt a real
// container run produces still goes through.
func TestCoherentContainerReceiptIsAccepted(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "7e")
	priv, _ := newPeer(t)

	var out verifyResponse
	r := signedReceipt(t, priv, sampleID, containerEnv(), "PASS")
	if r.SandboxCapability != domain.CapContainerRun {
		t.Fatalf("fixture is not a container receipt: %q", r.SandboxCapability)
	}
	if resp := postJSON(t, srv.URL+"/v1/verifications", r, &out); resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 for an honest container receipt", resp.StatusCode)
	}
	rows, err := store.ReceiptsForSample(t.Context(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("stored %d receipts, want 1", len(rows))
	}
}
