package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The producer writes the row's environment as "envBucket"; the page read
// it as "env". It never decoded, so every matrix row on every package page
// rendered with no environment detail — two buckets differing only by
// libc, container runtime or OS appeared as identical rows with different
// confidence chips and no way to tell which was which.
//
// This feeds the page from the REAL producer rather than a hand-written
// fixture, which is why the existing tests missed it: internal/web's fake
// hand-writes "envLabel".
func TestAMatrixRowShowsItsEnvironment(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
		Runtime: "node", RuntimeVersion: "22.18.1", Language: "javascript",
		LanguageVersion: "es2024", PackageManager: "npm", Libc: "musl",
	}.Normalize()
	rows := []serverstore.EvidenceRow{{
		PURL: "pkg:npm/esbuild@0.25.0", Symbol: "build",
		EnvHash: env.Hash(), EnvJSON: string(domain.MustCanonicalJSON(env)),
		Stage: string(domain.StageProjectCompile), Result: string(domain.ResultPass),
		ObservationCount: 12, UniquePeerBuckets: 2, SymbolConfidence: "PROBABLE",
		LastSeen: now,
	}}
	snap := compatibility.BuildSnapshot("pkg:npm/esbuild@0.25.0", "build", rows, nil, nil, now)
	js, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		Rows []snapshotRow `json:"rows"`
	}
	if err := json.Unmarshal(js, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Rows) == 0 {
		t.Fatal("the producer emitted no rows")
	}
	_, detail := rowLabels(decoded.Rows[0])
	if detail == "" {
		t.Fatalf("the row shows no environment at all: %+v", decoded.Rows[0])
	}
	if !strings.Contains(detail, "musl") {
		t.Errorf("the libc is missing from %q — musl vs glibc is the decisive dimension", detail)
	}
}
