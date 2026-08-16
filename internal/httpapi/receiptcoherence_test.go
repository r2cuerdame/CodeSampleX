package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
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

func TestLegacyV1ReceiptStillUsesOriginalSigningPayload(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "7f")
	priv, _ := newPeer(t)

	r := signedReceipt(t, priv, sampleID, containerEnv(), "PASS")
	if r.SchemaVersion != 1 {
		t.Fatalf("legacy fixture schemaVersion = %d, want 1", r.SchemaVersion)
	}
	if strings.Contains(string(r.SigningBytes()), `"resolvedPackages"`) {
		t.Fatal("empty resolvedPackages changed the v1 signing payload")
	}
	var out verifyResponse
	if resp := postJSON(t, srv.URL+"/v1/verifications", r, &out); resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 for a legacy v1 receipt", resp.StatusCode)
	}
}

func TestV1ReceiptCannotOptIntoV2ResolvedPackages(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "80")
	priv, _ := newPeer(t)

	r := signedReceipt(t, priv, sampleID, containerEnv(), "PASS")
	r.ResolvedPackages = []string{"pkg:npm/axios@1.13.0"}
	resignReceipt(priv, &r)

	var out struct {
		Error string `json:"error"`
	}
	resp := postJSON(t, srv.URL+"/v1/verifications", r, &out)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(out.Error, "schemaVersion 1") {
		t.Fatalf("error = %q, want a v1 schema boundary error", out.Error)
	}
}

func TestV2ReceiptRejectsPresentEmptyResolvedPackages(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "81")
	priv, _ := newPeer(t)

	r := signedReceipt(t, priv, sampleID, containerEnv(), "PASS")
	r.SchemaVersion = 2
	resignReceipt(priv, &r)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["resolvedPackages"] = []string{}

	var out struct {
		Error string `json:"error"`
	}
	resp := postJSON(t, srv.URL+"/v1/verifications", document, &out)
	if resp.StatusCode != 400 || !strings.Contains(out.Error, "omitted rather than empty") {
		t.Fatalf("status = %d, error = %q; want present-empty rejection", resp.StatusCode, out.Error)
	}
}

func TestVerificationReceiptRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "82")
	priv, _ := newPeer(t)
	r := signedReceipt(t, priv, sampleID, containerEnv(), "PASS")

	t.Run("unknown field", func(t *testing.T) {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		document["unsignedExtra"] = "discard me"
		var out struct {
			Error string `json:"error"`
		}
		resp := postJSON(t, srv.URL+"/v1/verifications", document, &out)
		if resp.StatusCode != 400 || !strings.Contains(out.Error, "unknown field") {
			t.Fatalf("status = %d, error = %q; want unknown-field rejection", resp.StatusCode, out.Error)
		}
	})

	t.Run("trailing document", func(t *testing.T) {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, []byte("\n{}")...)
		resp, err := http.Post(srv.URL+"/v1/verifications", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 400 || !strings.Contains(string(body), "exactly one JSON document") {
			t.Fatalf("status = %d, body = %q; want trailing-document rejection", resp.StatusCode, body)
		}
	})
}

func resignReceipt(priv ed25519.PrivateKey, r *domain.VerificationReceipt) {
	r.PeerSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, r.SigningBytes()))
}

func TestResolvedPackagesMustBeCoherentWithReceiptAndSample(t *testing.T) {
	tests := []struct {
		name     string
		resolved []string
		resolve  string
		wantErr  string
	}{
		{
			name: "failed resolve",
			resolved: []string{
				"pkg:npm/axios@1.13.0",
			},
			resolve: "FAIL",
			wantErr: "PASS resolve stage",
		},
		{
			name: "skipped resolve",
			resolved: []string{
				"pkg:npm/axios@1.13.0",
			},
			resolve: "SKIPPED",
			wantErr: "PASS resolve stage",
		},
		{
			name: "invalid purl",
			resolved: []string{
				"not-a-purl",
			},
			resolve: "PASS",
			wantErr: "invalid purl",
		},
		{
			name: "noncanonical purl",
			resolved: []string{
				"pkg:NPM/axios@1.13.0",
			},
			resolve: "PASS",
			wantErr: "canonical purls",
		},
		{
			name: "version request rather than resolved version",
			resolved: []string{
				"pkg:npm/axios@^1.13.0",
			},
			resolve: "PASS",
			wantErr: "concrete resolved versions",
		},
		{
			name: "duplicate purl",
			resolved: []string{
				"pkg:npm/axios@1.13.0",
				"pkg:npm/axios@1.13.0",
			},
			resolve: "PASS",
			wantErr: "duplicates",
		},
		{
			name: "unsorted purls",
			resolved: []string{
				"pkg:npm/zod@4.0.0",
				"pkg:npm/axios@1.13.0",
			},
			resolve: "PASS",
			wantErr: "sorted",
		},
		{
			name: "undeclared package",
			resolved: []string{
				"pkg:npm/zod@4.0.0",
			},
			resolve: "PASS",
			wantErr: "not declared",
		},
		{
			name: "lockfile from a resolver that did not run",
			resolved: []string{
				"pkg:cargo/serde@1.0.229",
			},
			resolve: "PASS",
			wantErr: "resolver that did not run",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, _ := newTestServer(t, nil)
			sampleID := saveSampleForVerification(t, store, "8a")
			priv, _ := newPeer(t)

			r := signedReceipt(t, priv, sampleID, containerEnv(), "PASS")
			r.SchemaVersion = 2
			r.Stages["resolve"] = tc.resolve
			r.ResolvedPackages = tc.resolved
			resignReceipt(priv, &r)

			var out struct {
				Error string `json:"error"`
			}
			resp := postJSON(t, srv.URL+"/v1/verifications", r, &out)
			if resp.StatusCode != 400 {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			if !strings.Contains(out.Error, tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", out.Error, tc.wantErr)
			}
			rows, err := store.ReceiptsForSample(t.Context(), sampleID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Fatalf("refused receipt was stored: %d rows", len(rows))
			}
		})
	}
}

func TestResolvedPackagesMayRecordADifferentVersion(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "8b")
	priv, _ := newPeer(t)

	r := signedReceipt(t, priv, sampleID, containerEnv(), "PASS")
	r.SchemaVersion = 2
	r.ResolvedPackages = []string{"pkg:npm/axios@1.13.0"}
	resignReceipt(priv, &r)

	var out verifyResponse
	if resp := postJSON(t, srv.URL+"/v1/verifications", r, &out); resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 for a resolved version differing from the manifest", resp.StatusCode)
	}
	rows, err := store.ReceiptsForSample(t.Context(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].ReceiptJSON, `"resolvedPackages":["pkg:npm/axios@1.13.0"]`) {
		t.Fatalf("resolved package was not preserved: %+v", rows)
	}
}
