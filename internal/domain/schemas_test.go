package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// schemaDir walks up from the package dir to the repo root schemas/v1.
func schemaDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		cand := filepath.Join(dir, "schemas", "v1")
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return cand
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("schemas/v1 not found")
	return ""
}

// TestSchemaFixtures checks every schema file parses and that each schema's
// required properties exist in the JSON produced by the matching Go type.
func TestSchemaFixtures(t *testing.T) {
	dir := schemaDir(t)
	fixtures := map[string]any{
		"environment.json": EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64"},
		"observation-batch.json": ObservationBatch{SchemaVersion: 1, Epoch: "2026-08-13",
			AnonID: "0123456789abcdef", ProjectBucket: "0123456789ab", Package: "pkg:npm/axios@1.12.0",
			Environment: EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64"},
			Stage:       StageProjectCompile, Result: ResultPass, ObservationCount: 3},
		"case.json": Case{SchemaVersion: 1, Kind: "HOW", Goal: "g",
			Packages: []string{"pkg:npm/axios@1.12.0"}, Contract: []string{"c"}},
		"sample-manifest.json": SampleManifest{SchemaVersion: 1,
			Case:        Case{SchemaVersion: 1, Kind: "HOW", Goal: "g", Packages: []string{"p"}, Contract: []string{"c"}},
			Packages:    []string{"pkg:npm/axios@1.12.0"},
			Environment: EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64"},
			License:     "MIT-0", ContractCommand: []string{"node", "test/contract.mjs"}, VerifierAdapter: "node-typescript@1"},
		"verification-receipt.json": VerificationReceipt{SchemaVersion: 1, SampleID: "sha256:ab", CaseID: "case:x",
			EnvironmentHash: "sha256:cd",
			Environment:     EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64"},
			Stages:          map[string]string{"resolve": "PASS"}, VerifierAdapter: "node-typescript@1",
			SandboxCapability: CapContainerRun, LogsDigest: "sha256:ef", CreatedAt: "2026-08-13T00:00:00Z",
			PeerID: "ed25519:0123456789abcdef", PeerPubkey: "pk", PeerSignature: "sig"},
		"search-request.json": SearchRequest{SchemaVersion: 1, Query: "q",
			Environment: EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64"}},
		"search-response.json": SearchResponse{SchemaVersion: 1, Results: []SearchResult{}, Miss: true},
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 10 {
		t.Fatalf("expected >=10 schema files, found %d", len(entries))
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: not valid JSON: %v", e.Name(), err)
		}
		fixture, ok := fixtures[e.Name()]
		if !ok {
			continue
		}
		req, _ := schema["required"].([]any)
		var m map[string]any
		b, _ := json.Marshal(fixture)
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		for _, r := range req {
			key := r.(string)
			if _, present := m[key]; !present {
				t.Errorf("%s: required key %q missing from Go type's JSON", e.Name(), key)
			}
		}
	}
}
