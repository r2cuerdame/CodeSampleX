package web

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestDumpSampleReview writes the sample page rendered with a digest-pinned
// verifier image to CSX_SAMPLE_DUMP, for looking at rather than asserting on.
// Skipped unless set.
func TestDumpSampleReview(t *testing.T) {
	dir := os.Getenv("CSX_SAMPLE_DUMP")
	if dir == "" {
		t.Skip("CSX_SAMPLE_DUMP not set")
	}
	mux, st := newTestMux(t, nil)
	st.receipts["sha256:d1e2f3"] = []string{`{
	  "schemaVersion": 2, "sampleId": "sha256:d1e2f3", "caseId": "case:sha256:9999",
	  "environmentHash": "sha256:eeee",
	  "environment": {"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"amd64",
	    "runtime":"node","runtimeVersion":"22.18","executionContext":"node"},
	  "stages": {"resolve":"PASS","compile":"PASS","contract":"PASS"},
	  "verifierImage": {"reference":"node:22-alpine@` + sampleImageDigest + `","digest":"` + sampleImageDigest + `"},
	  "verifierAdapter": "node-typescript@1", "sandboxCapability": "CONTAINER_RUN",
	  "logsDigest": "sha256:ffff", "createdAt": "2026-08-02T00:00:00Z",
	  "peerId": "ed25519:0011223344556677", "peerPubkey": "cHVi", "peerSignature": "c2ln"
	}`}
	rec := get(t, mux, "/samples/sha256:d1e2f3")
	body := bytes.ReplaceAll(rec.Body.Bytes(), []byte(`"/static/`), []byte(`"static/`))
	if err := os.WriteFile(filepath.Join(dir, "sample.html"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile(filepath.Join("static", "site.css"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "static", "site.css"), css, 0o644); err != nil {
		t.Fatal(err)
	}
}
