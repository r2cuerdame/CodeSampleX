package search

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func browserEnv(family, major string) domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "macos", Arch: "arm64",
		ModuleSystem: "esm", ExecutionContext: "browser",
		BrowserFamily: family, BrowserMajor: major,
	}
}

// Scenario: safari 15 sample, safari 19 caller, exact package version.
func TestProbeBrowserMajorDelta(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := mkManifest("fetch json with axios",
		[]string{"pkg:npm/axios@1.12.0"}, browserEnv("safari", "15"), "axios.get")
	if err := SeedSampleDoc(ctx, db, m, "sha256:b001", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "fetch json with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   browserEnv("safari", "19"),
	})
	if resp.Miss || len(resp.Results) == 0 {
		t.Fatalf("miss=%v results=%d", resp.Miss, len(resp.Results))
	}
	r := resp.Results[0]
	t.Logf("GRADE=%s\n  Exact=%v\n  Different=%v\n  Adaptation=%v", r.Grade, r.Exact, r.Different, r.Adaptation)
}

// Scenario: chrome sample, edge 140 caller (same chromium engine).
func TestProbeBrowserFamilySameEngine(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := mkManifest("fetch json with axios",
		[]string{"pkg:npm/axios@1.12.0"}, browserEnv("chrome", "140"), "axios.get")
	if err := SeedSampleDoc(ctx, db, m, "sha256:b002", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "fetch json with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   browserEnv("edge", "140"),
	})
	if resp.Miss || len(resp.Results) == 0 {
		t.Fatalf("miss=%v results=%d", resp.Miss, len(resp.Results))
	}
	r := resp.Results[0]
	t.Logf("GRADE=%s\n  Exact=%v\n  Different=%v\n  Adaptation=%v", r.Grade, r.Exact, r.Different, r.Adaptation)
}

// Unit-level: does buildDelta drop the browserAdapt context pair?
func TestProbeBuildDeltaBrowserAdapt(t *testing.T) {
	req := browserEnv("safari", "19")
	sam := browserEnv("safari", "15")
	cd := compareContext(req, sam)
	t.Logf("cd = %+v", cd)
	dims := compareEnv(req, sam, "npm")
	g, ad := buildGrade(relExactVersion, dims, cd, false)
	ex, diff := buildDelta(relExactVersion,
		domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"},
		domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"},
		dims, cd)
	t.Logf("grade=%s adapt=%v exact=%v different=%v", g, ad, ex, diff)
}
