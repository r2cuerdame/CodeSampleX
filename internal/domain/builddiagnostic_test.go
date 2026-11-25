package domain

import (
	"bytes"
	"testing"
)

func TestReceiptWithoutDiagnosticsKeepsSigningBytes(t *testing.T) {
	r := VerificationReceipt{SchemaVersion: 2, SampleID: "sha256:ab", CaseID: "case:x", EnvironmentHash: "sha256:cd",
		Environment: EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64"},
		Stages:      map[string]string{"resolve": "PASS"}, VerifierAdapter: "node-typescript@1",
		SandboxCapability: CapContainerRun, LogsDigest: "sha256:ef", CreatedAt: "2026-08-13T00:00:00Z",
		PeerID: "ed25519:0123456789abcdef", PeerPubkey: "pk"}
	if bytes.Contains(r.SigningBytes(), []byte("diagnostics")) {
		t.Fatal("a receipt signed before diagnostics existed would no longer verify")
	}
	r.Diagnostics = []BuildDiagnostic{{Tool: "go/vet", Severity: BuildDiagnosticWarning, Kind: BuildDiagnosticDeprecated,
		Message: "m", RawLogDigest: "sha256:ab"}}
	if !bytes.Contains(r.SigningBytes(), []byte(`"diagnostics"`)) {
		t.Fatal("diagnostics are not covered by the peer signature")
	}
}
