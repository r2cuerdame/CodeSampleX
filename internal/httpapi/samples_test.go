package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestSampleUploadHappyPath(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, map[string]string{
		"src/index.mjs":     "import axios from 'axios';\nexport const post = axios.post;\n",
		"test/contract.mjs": "console.log('contract');\n",
	})
	sampleID := domain.SHA256Hex(artifact)

	resp := postSample(t, srv.URL, manifest, sampleID, artifact, "")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	var created struct{ SampleID, Status string }
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.SampleID != sampleID || created.Status != "PUBLISHED" {
		t.Fatalf("created = %+v", created)
	}

	// A cross-verification job was queued.
	jobs, err := store.JobsForSample(context.Background(), sampleID)
	if err != nil || len(jobs) != 1 || jobs[0].Reason != "cross" || jobs[0].Status != "open" {
		t.Fatalf("jobs = %+v err=%v, want one open cross job", jobs, err)
	}

	// Metadata reads back with anonymous seeder.
	var meta struct {
		SampleID     string          `json:"sampleId"`
		Status       string          `json:"status"`
		License      string          `json:"license"`
		OriginSeeder string          `json:"originSeeder"`
		Manifest     json.RawMessage `json:"manifest"`
		Receipts     []any           `json:"receipts"`
	}
	getJSON(t, srv.URL+"/v1/samples/"+sampleID, &meta)
	if meta.Status != "PUBLISHED" || meta.License != "MIT-0" || meta.OriginSeeder != "anonymous" {
		t.Fatalf("meta = %+v", meta)
	}
	if len(meta.Manifest) == 0 {
		t.Fatal("meta must embed the manifest")
	}

	// Artifact streams back byte-identical (Main Seeder fallback).
	aresp, err := http.Get(srv.URL + "/v1/samples/" + sampleID + "/artifact")
	if err != nil {
		t.Fatal(err)
	}
	defer aresp.Body.Close()
	if aresp.StatusCode != http.StatusOK {
		t.Fatalf("artifact status = %d", aresp.StatusCode)
	}
	if ct := aresp.Header.Get("Content-Type"); ct != "application/gzip" {
		t.Fatalf("content-type = %q", ct)
	}
	got, _ := io.ReadAll(aresp.Body)
	if !bytes.Equal(got, artifact) {
		t.Fatal("artifact bytes differ")
	}
	if domain.SHA256Hex(got) != sampleID {
		t.Fatal("artifact hash mismatch after round trip")
	}
}

func TestSampleUploadRejectsSampleIDMismatch(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, nil)
	wrongID := "sha256:" + strings.Repeat("00", 32)

	resp := postSample(t, srv.URL, manifest, wrongID, artifact, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "does not match") {
		t.Fatalf("body = %s", body)
	}
}

func TestSampleUploadRejectsOversizedArtifact(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()
	// >256KB payload (contents are irrelevant — the size gate fires first).
	big := bytes.Repeat([]byte("0123456789abcdef"), (maxArtifactBytes/16)+64)
	resp := postSample(t, srv.URL, manifest, domain.SHA256Hex(big), big, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "256KB") {
		t.Fatalf("body = %s", body)
	}
}

func TestSampleUploadRejectsForbiddenContents(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()

	cases := []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"forbidden dir", map[string]string{"node_modules/x/index.js": "x"}, "forbidden"},
		{"traversal", map[string]string{"../evil.js": "x"}, "traversal"},
		{"env file", map[string]string{".env": "SECRET=1"}, ".env"},
		{"binary", map[string]string{"blob.bin": "abc\x00def"}, "binary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact := buildArtifact(t, manifest, tc.extra)
			resp := postSample(t, srv.URL, manifest, domain.SHA256Hex(artifact), artifact, "")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), tc.want) {
				t.Fatalf("body = %s, want mention of %q", body, tc.want)
			}
		})
	}
}

func TestSampleUploadRejectsMissingOrMismatchedCsxJSON(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()

	// Artifact whose csx.json differs from the posted manifest.
	other := manifest
	other.Case.Goal = "something else entirely"
	artifact := buildArtifact(t, other, nil)
	resp := postSample(t, srv.URL, manifest, domain.SHA256Hex(artifact), artifact, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "does not match the posted manifest") {
		t.Fatalf("body = %s", body)
	}

	// Artifact without csx.json at all.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte{})
	gz.Close()
	noCsx := buf.Bytes()
	resp = postSample(t, srv.URL, manifest, domain.SHA256Hex(noCsx), noCsx, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSampleUploadRejectsNonPermissiveLicense(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()
	manifest.License = "GPL-3.0-only"
	artifact := buildArtifact(t, manifest, nil)
	resp := postSample(t, srv.URL, manifest, domain.SHA256Hex(artifact), artifact, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "license") {
		t.Fatalf("body = %s", body)
	}
}

func TestSampleUploadRejectsUnknownBearerToken(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, nil)
	resp := postSample(t, srv.URL, manifest, domain.SHA256Hex(artifact), artifact, "csx_bogus")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSampleMetaNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	resp, _ := http.Get(srv.URL + "/v1/samples/sha256:" + strings.Repeat("ff", 32))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
