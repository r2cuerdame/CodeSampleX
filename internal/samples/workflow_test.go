package samples

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func testManifest() domain.SampleManifest {
	return domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1,
			Kind:          "HOW",
			Goal:          "Echo a string",
			Packages:      []string{"pkg:npm/axios@1.12.0"},
			Contract:      []string{"echo returns its input"},
		},
		Packages: []string{"pkg:npm/axios@1.12.0"},
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22.18.0",
		},
		ContractCommand: []string{"node", "test/contract.mjs"},
		VerifierAdapter: "node-typescript@1",
	}
}

func TestWorkflowCreateFromDirDefaults(t *testing.T) {
	dir := sampleFixture(t)
	m := testManifest()
	m.License = "" // must default to MIT-0

	created, err := CreateFromDir(context.Background(), dir, m)
	if err != nil {
		t.Fatal(err)
	}
	if created.Manifest.License != "MIT-0" {
		t.Fatalf("license %q, want MIT-0", created.Manifest.License)
	}
	if created.MaxLevel != domain.L5MatrixPass {
		t.Fatalf("max level %s, want %s", created.MaxLevel, domain.L5MatrixPass)
	}
	if created.SampleID != domain.SHA256Hex(created.Artifact) {
		t.Fatal("sample id != artifact hash")
	}
	if created.Manifest.Case.CaseID == "" {
		t.Fatal("case id not computed")
	}
	if len(created.Findings) != 0 {
		t.Fatalf("clean fixture produced findings: %+v", created.Findings)
	}

	// csx.json is written into the dir and round-trips as the manifest.
	raw, err := os.ReadFile(filepath.Join(dir, "csx.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk domain.SampleManifest
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.License != "MIT-0" || onDisk.Case.CaseID != created.Manifest.Case.CaseID {
		t.Fatalf("csx.json does not match manifest: %+v", onDisk)
	}
}

func TestWorkflowContractlessCapsAtL2(t *testing.T) {
	dir := sampleFixture(t)
	m := testManifest()
	m.ContractCommand = nil

	created, err := CreateFromDir(context.Background(), dir, m)
	if err != nil {
		t.Fatal(err)
	}
	if created.MaxLevel != domain.L2Compiled {
		t.Fatalf("contract-less max level %s, want %s", created.MaxLevel, domain.L2Compiled)
	}
}

func TestWorkflowRecomputesStaleCaseID(t *testing.T) {
	dir := sampleFixture(t)
	m := testManifest()
	m.Case.CaseID = "case:sha256:" + strings.Repeat("0", 64)
	want := m.Case.ComputeID()

	created, err := CreateFromDir(context.Background(), dir, m)
	if err != nil {
		t.Fatal(err)
	}
	if got := created.Manifest.Case.CaseID; got != want {
		t.Fatalf("case id = %q, want recomputed %q", got, want)
	}
	if got := created.Manifest.Case.CaseID; got == m.Case.CaseID {
		t.Fatal("stale author-supplied case id was preserved")
	}
}

func TestWorkflowFindingsDoNotBlockCreation(t *testing.T) {
	dir := sampleFixture(t)
	writeFile(t, dir, "src/leak.js", `const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789";`)

	created, err := CreateFromDir(context.Background(), dir, testManifest())
	if err != nil {
		t.Fatalf("findings must not block creation: %v", err)
	}
	if len(created.Findings) == 0 {
		t.Fatal("expected leakage findings to be recorded")
	}
}

func TestWorkflowRejectsBadManifests(t *testing.T) {
	dir := sampleFixture(t)

	m := testManifest()
	m.Packages = nil
	if _, err := CreateFromDir(context.Background(), dir, m); err == nil {
		t.Fatal("expected error for manifest without packages")
	}

	m = testManifest()
	m.Case.Goal = ""
	if _, err := CreateFromDir(context.Background(), dir, m); err == nil {
		t.Fatal("expected error for manifest without a goal")
	}
}

func TestWorkflowNewCleanRoom(t *testing.T) {
	home := t.TempDir()
	work, err := NewCleanRoom(home)
	if err != nil {
		t.Fatal(err)
	}
	wantBase := filepath.Join(home, "samples", "work")
	if !strings.HasPrefix(work, wantBase) {
		t.Fatalf("clean room %s not under %s", work, wantBase)
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("clean room not empty: %v", entries)
	}
	// Two clean rooms never collide.
	work2, err := NewCleanRoom(home)
	if err != nil {
		t.Fatal(err)
	}
	if work2 == work {
		t.Fatal("clean rooms collide")
	}
}
