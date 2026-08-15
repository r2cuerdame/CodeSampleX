package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/search"
	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// seedHome builds a CSX home with one cached sample (metadata + artifact)
// and one cached shard, then closes its handles so NewDeps can re-open.
func seedHome(t *testing.T) (home, sampleID string) {
	t.Helper()
	home = t.TempDir()
	ctx := context.Background()

	// A community install: uploads are part of what this wiring test
	// exercises, and nothing is queued for a user who has not joined.
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	if err := cfg.Save(home); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// Author a tiny sample and store its canonical artifact in the CAS.
	dir := t.TempDir()
	big := strings.Repeat("// filler line for the 64KB cap test\n", 2200) // ~84KB
	writeFile(t, dir, "index.mjs", "import axios from 'axios'\nexport const ok = true\n")
	writeFile(t, dir, "big.txt", big)
	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW", Goal: "axios post basics",
			Packages: []string{"pkg:npm/axios@1.12.0"}, Contract: []string{"posts JSON"},
		},
		Packages:        []string{"pkg:npm/axios@1.12.0"},
		Environment:     domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64", Runtime: "node"},
		ContractCommand: []string{"node", "test/contract.mjs"},
	}
	created, err := samples.CreateFromDir(ctx, dir, manifest)
	if err != nil {
		t.Fatalf("CreateFromDir: %v", err)
	}
	store, err := cas.Open(filepath.Join(home, "cas"))
	if err != nil {
		t.Fatalf("cas.Open: %v", err)
	}
	if _, err := store.Put(bytes.NewReader(created.Artifact)); err != nil {
		t.Fatalf("cas.Put: %v", err)
	}

	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("localdb.Open: %v", err)
	}
	defer db.Close()
	if err := search.SeedSampleDoc(ctx, db, created.Manifest, created.SampleID, "LOCAL_PASS"); err != nil {
		t.Fatalf("SeedSampleDoc: %v", err)
	}

	shard := `{"schemaVersion":1,"key":"npm/axios/1","generatedAt":"2026-08-13T00:00:00Z",
	 "packages":[{"purl":"pkg:npm/axios@1.12.0","symbols":[
	   {"family":"axios.post","stats":{"observationCount":123,"uniquePeerBuckets":9,"passRate":0.94,
	    "byStage":{"PROJECT_COMPILE":{"pass":100,"fail":4}},"confidence":"HIGH","lastSeen":"2026-08-01T00:00:00Z"},
	    "failures":[{"errorCode":"ERR_REQUIRE_ESM","fingerprint":"sha256:aa","count":7,
	                 "envSummary":{"moduleSystem":"esm","runtime":"node@18"}}]}],
	  "samples":[{"sampleId":"` + created.SampleID + `","goal":"axios post basics","status":"CROSS_PASS",
	              "license":"MIT-0","environment":{"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"amd64"},
	              "contractStages":{"contract":"PASS"}}]}]}`
	if err := db.SaveShard(ctx, "npm/axios/1", `W/"1"`, shard); err != nil {
		t.Fatalf("SaveShard: %v", err)
	}
	return home, created.SampleID
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestNewDepsRealWiring(t *testing.T) {
	home, sampleID := seedHome(t)
	deps, closeDB, err := NewDeps(home)
	if err != nil {
		t.Fatalf("NewDeps: %v", err)
	}
	defer closeDB() //nolint:errcheck
	ctx := context.Background()

	t.Run("GetSample", func(t *testing.T) {
		manifest, files, err := deps.GetSample(ctx, sampleID)
		if err != nil {
			t.Fatalf("GetSample: %v", err)
		}
		if manifest.Case.Goal != "axios post basics" {
			t.Errorf("manifest goal = %q", manifest.Case.Goal)
		}
		if _, ok := files["index.mjs"]; !ok {
			t.Errorf("files missing index.mjs: %v", keysOf(files))
		}
		if _, ok := files["csx.json"]; !ok {
			t.Errorf("files missing csx.json: %v", keysOf(files))
		}
		big, ok := files["big.txt"]
		if !ok {
			t.Fatalf("files missing big.txt")
		}
		if len(big) > maxFileReturn+64 {
			t.Errorf("big.txt not capped: %d bytes", len(big))
		}
		if !strings.Contains(big, "[truncated at 64KB]") {
			t.Errorf("big.txt missing truncation marker")
		}
	})

	t.Run("GetSampleMissing", func(t *testing.T) {
		_, _, err := deps.GetSample(ctx, "sha256:"+strings.Repeat("0", 64))
		if err == nil {
			t.Fatalf("expected error for unknown sample")
		}
	})

	t.Run("Search", func(t *testing.T) {
		resp := deps.Search(ctx, domain.SearchRequest{
			SchemaVersion: 1,
			Query:         "axios post basics",
			Packages:      []string{"pkg:npm/axios@1.12.0"},
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64", Runtime: "node",
			},
		})
		if resp.Miss || len(resp.Results) == 0 {
			t.Fatalf("expected a hit for the seeded sample, got miss")
		}
	})

	t.Run("Explain", func(t *testing.T) {
		text, snapshot, err := deps.Explain(ctx, "pkg:npm/axios@1.12.0", "axios.post", domain.EnvironmentFingerprint{})
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		for _, want := range []string{
			"USAGE_OBSERVATION",
			"SAMPLE_VERIFICATION",
			"PROJECT_COMPILE: 100 pass / 4 fail",
			"ERR_REQUIRE_ESM",
			"contract PASS",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("Explain text missing %q:\n%s", want, text)
			}
		}
		if !json.Valid(snapshot) || string(snapshot) == "null" {
			t.Errorf("Explain snapshot invalid: %s", snapshot)
		}
	})

	t.Run("ExplainNoShard", func(t *testing.T) {
		text, snapshot, err := deps.Explain(ctx, "pkg:npm/left-pad@1.3.0", "", domain.EnvironmentFingerprint{})
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		if !strings.Contains(text, "UNKNOWN") {
			t.Errorf("no-data text must say UNKNOWN, got:\n%s", text)
		}
		if string(snapshot) != "null" {
			t.Errorf("no-data snapshot = %s, want null", snapshot)
		}
	})

	t.Run("ReportAdoptionAndHitsAndStats", func(t *testing.T) {
		pass := true
		if err := deps.ReportAdoption(ctx, sampleID, true, &pass); err != nil {
			t.Fatalf("ReportAdoption: %v", err)
		}
		hits, err := deps.LocalHits(ctx)
		if err != nil || len(hits) == 0 {
			t.Fatalf("LocalHits after adoption: %v, %v", hits, err)
		}
		if hits[0].SampleID != sampleID || !hits[0].Adopted {
			t.Errorf("hit row = %+v", hits[0])
		}
		if !hits[0].PostBuildPass.Valid || !hits[0].PostBuildPass.Bool {
			t.Errorf("hit postBuildPass = %+v, want true", hits[0].PostBuildPass)
		}

		stats, err := deps.LocalStats(ctx)
		if err != nil {
			t.Fatalf("LocalStats: %v", err)
		}
		if stats["hits"].(int) < 1 {
			t.Errorf("stats.hits = %v, want ≥1", stats["hits"])
		}
		if stats["queuedUploads"].(int) < 1 {
			t.Errorf("stats.queuedUploads = %v, want ≥1 (adoption evidence enqueued)", stats["queuedUploads"])
		}
		if stats["mode"] != "community" {
			t.Errorf("stats.mode = %v", stats["mode"])
		}
	})

	t.Run("AdoptionQueuePayloadIsAnonymous", func(t *testing.T) {
		db, err := localdb.Open(filepath.Join(home, "csx.db"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()
		items, err := db.QueuePending(ctx, 10)
		if err != nil || len(items) == 0 {
			t.Fatalf("QueuePending: %v, %v", items, err)
		}
		var payload adoptionPayload
		if err := json.Unmarshal([]byte(items[0].Payload), &payload); err != nil {
			t.Fatalf("payload not JSON: %v", err)
		}
		if items[0].Kind != "adoption" {
			t.Errorf("queue kind = %q, want adoption", items[0].Kind)
		}
		if payload.EvidenceClass != "ADOPTION_EVIDENCE" || payload.SampleID != sampleID || payload.AnonID == "" {
			t.Errorf("payload = %+v", payload)
		}
		if strings.Contains(items[0].Payload, home) {
			t.Errorf("adoption payload leaks home path: %s", items[0].Payload)
		}
	})

	t.Run("Propose", func(t *testing.T) {
		spec, prompt, workdir, err := deps.Propose(ctx, "axios upload", []string{"pkg:npm/axios@1.12.0"}, []string{"axios.post"})
		if err != nil {
			t.Fatalf("Propose: %v", err)
		}
		if len(spec.Packages) != 1 || spec.Packages[0] != "pkg:npm/axios@1.12.0" {
			t.Errorf("spec packages = %v", spec.Packages)
		}
		if !strings.Contains(prompt, "axios upload") {
			t.Errorf("prompt missing goal")
		}
		info, err := os.Stat(workdir)
		if err != nil || !info.IsDir() {
			t.Errorf("workdir %q not created: %v", workdir, err)
		}
		entries, _ := os.ReadDir(workdir)
		if len(entries) != 0 {
			t.Errorf("clean room not empty: %v", entries)
		}
		if !strings.HasPrefix(workdir, filepath.Join(home, "samples", "work")) {
			t.Errorf("workdir %q outside home clean-room base", workdir)
		}
	})

	t.Run("ProposeRejectsNonPURL", func(t *testing.T) {
		_, _, _, err := deps.Propose(ctx, "goal", []string{"axios"}, nil)
		if err == nil {
			t.Fatalf("expected error for non-purl package")
		}
	})
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// An adoption is not a search; it is what happened to one. Reporting one
// used to INSERT its own row, so a single search that was then adopted
// counted as two hits — the search row, plus a second row with an empty
// query and an empty grade — and csx stats reported the doubled number.
func TestAdoptionUpdatesTheSearchInsteadOfCountingTwice(t *testing.T) {
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	id := "sha256:" + strings.Repeat("12", 32)

	if err := db.RecordHit(ctx, localdb.HitRow{
		TS: time.Now().UTC(), Query: "axios post basics",
		Grade: domain.GradeExact, SampleID: id,
	}); err != nil {
		t.Fatal(err)
	}
	pass := true
	ident, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reportAdoption(ctx, db, ident, &config.Config{Mode: config.ModeCommunity}, id, true, &pass); err != nil {
		t.Fatal(err)
	}

	n, err := db.CountHits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		rows, _ := db.ListHits(ctx, 10)
		for i, r := range rows {
			t.Logf("row %d: query=%q grade=%q adopted=%v", i, r.Query, r.Grade, r.Adopted)
		}
		t.Errorf("CountHits = %d, want 1: one search that was then adopted", n)
	}
	adopted, err := db.CountAdoptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if adopted != 1 {
		t.Errorf("CountAdoptions = %d, want 1", adopted)
	}

	// An adoption with no preceding search on this machine is a real event
	// and still gets a row — an agent can obtain a sample another way.
	other := "sha256:" + strings.Repeat("ab", 32)
	if err := reportAdoption(ctx, db, ident, &config.Config{Mode: config.ModeCommunity}, other, true, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.CountHits(ctx); n != 2 {
		t.Errorf("CountHits = %d after an unsolicited adoption, want 2", n)
	}
}
