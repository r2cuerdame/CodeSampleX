package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/storage/blob"
)

var testNow = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

// clock is a mutable test clock shared between Deps.Now and the fake store.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// newTestServer builds a full API server over the in-memory fake store in
// trust mode with a temp blob dir. mutate customizes Deps before the mux is
// built.
func newTestServer(t *testing.T, mutate func(*Deps)) (*httptest.Server, *serverstore.Fake, *clock) {
	t.Helper()
	store := serverstore.NewFake()
	ck := &clock{t: testNow}
	store.NowFn = ck.now
	blobs, err := blob.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Store: store,
		Blobs: blobs,
		Cfg:   serverstore.ServerConfig{PublicCheck: "trust"},
		Now:   ck.now,
		// Reachable by default so peer tests do not dial the network; the
		// unreachable path has its own test that stubs this false.
		PeerProbe: func(context.Context, string, int) bool { return true },
	}
	if mutate != nil {
		mutate(&deps)
	}
	srv := httptest.NewServer(NewMux(deps))
	t.Cleanup(srv.Close)
	return srv, store, ck
}

func nodeEnv(moduleSystem string) domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18.1", ModuleSystem: moduleSystem,
	}
}

func testBatch(pkg, symbol string, env domain.EnvironmentFingerprint,
	stage domain.Stage, result domain.Result, count int) domain.ObservationBatch {
	return domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-13", AnonID: "peer1", ProjectBucket: "proj1",
		Package: pkg, Symbol: symbol, SymbolConfidence: domain.SymbolProbable,
		Environment: env, Stage: stage, Result: result, ObservationCount: count,
	}
}

// postJSON posts v as JSON and decodes the reply into out (if non-nil).
func postJSON(t *testing.T, url string, v any, out any) *http.Response {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if out != nil {
		raw, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("POST %s: bad JSON reply %q: %v", url, raw, err)
		}
	}
	return resp
}

func getJSON(t *testing.T, url string, out any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if out != nil {
		raw, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("GET %s: bad JSON reply %q: %v", url, raw, err)
		}
	}
	return resp
}

// testManifest builds a minimal valid sample manifest.
func testManifest() domain.SampleManifest {
	return domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW", Goal: "post JSON with axios",
			Packages: []string{"pkg:npm/axios@1.12.0"},
			Contract: []string{"posts JSON body and returns parsed response"},
		},
		Packages:        []string{"pkg:npm/axios@1.12.0"},
		Symbols:         []string{"axios.post"},
		Environment:     nodeEnv("esm"),
		License:         "MIT-0",
		ContractCommand: []string{"node", "test/contract.mjs"},
		VerifierAdapter: "node-typescript@1",
	}
}

// buildArtifact renders a deterministic tar.gz whose csx.json equals the
// manifest, plus any extra files.
func buildArtifact(t *testing.T, manifest domain.SampleManifest, extra map[string]string) []byte {
	t.Helper()
	files := map[string]string{"csx.json": string(domain.MustCanonicalJSON(manifest))}
	for k, v := range extra {
		files[k] = v
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		content := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// postSample uploads a sample via multipart form.
func postSample(t *testing.T, srvURL string, manifest domain.SampleManifest,
	sampleID string, artifact []byte, token string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("manifest", string(manifestJSON)); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("sampleId", sampleID); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("artifact", "sample.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(artifact); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, srvURL+"/v1/samples", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// newPeer generates an ed25519 keypair with its fingerprint peer id.
func newPeer(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pub)
	return priv, "ed25519:" + hex.EncodeToString(sum[:])[:16]
}

// signWith signs the receipt's canonical signing bytes with priv.
func signWith(t *testing.T, priv ed25519.PrivateKey, r domain.VerificationReceipt) []byte {
	t.Helper()
	return ed25519.Sign(priv, r.SigningBytes())
}

// signedReceipt builds a correctly signed verification receipt.
func signedReceipt(t *testing.T, priv ed25519.PrivateKey, sampleID string,
	env domain.EnvironmentFingerprint, contract string) domain.VerificationReceipt {
	t.Helper()
	pub := priv.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	r := domain.VerificationReceipt{
		SchemaVersion:   1,
		SampleID:        sampleID,
		CaseID:          "case:sha256:test",
		EnvironmentHash: env.Normalize().Hash(),
		Environment:     env,
		Stages: map[string]string{
			"resolve": "PASS", "compile": "PASS", "contract": contract,
		},
		VerifierAdapter:   "node-typescript@1",
		SandboxCapability: domain.CapContainerRun,
		LogsDigest:        "sha256:" + hex.EncodeToString(bytes.Repeat([]byte{0x11}, 32)),
		CreatedAt:         testNow.Format(time.RFC3339),
		PeerID:            "ed25519:" + hex.EncodeToString(sum[:])[:16],
		PeerPubkey:        base64.StdEncoding.EncodeToString(pub),
	}
	r.PeerSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, r.SigningBytes()))
	return r
}
