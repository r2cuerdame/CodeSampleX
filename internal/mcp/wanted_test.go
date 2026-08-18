package mcp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Agents arrive over MCP, so this is the path where questions actually get
// asked — and it was the one path whose misses were thrown away. Confirmed
// live: a real agent, given the rule in a standing file, called
// search_known_solution first and got NO_SAFE_MATCH, and nothing recorded
// that anyone had wanted it.
func TestAnMCPMissQueuesWantedCandidateWithoutRegistryIO(t *testing.T) {
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	ctx := t.Context()

	req := domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "load a json config with dotenv",
		Packages:      []string{"pkg:npm/dotenv@17.2.3"},
		Symbols:       []string{"dotenv.config"},
		// The lockfile is context, never the question.
		ProjectPackages: []string{"pkg:npm/express@5.1.0"},
	}
	recordSearchOutcome(ctx, db, ident, cfg, req, domain.SearchResponse{SchemaVersion: 1, Miss: true})

	items, err := db.QueuePending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range items {
		if it.Kind != evidence.WantedCandidateQueueKind {
			continue
		}
		found = true
		var m map[string]any
		if err := json.Unmarshal([]byte(it.Payload), &m); err != nil {
			t.Fatal(err)
		}
		pkgs, _ := m["packages"].([]any)
		if len(pkgs) != 1 || pkgs[0] != "pkg:npm/dotenv@17.2.3" {
			t.Errorf("packages = %v; the lockfile must not be counted", pkgs)
		}
		if _, leaked := m["query"]; leaked {
			t.Error("the question itself was queued for upload")
		}
	}
	if !found {
		t.Fatal("a miss over MCP recorded nothing anyone can act on")
	}

	// A HIT is not a want.
	before := len(items)
	recordSearchOutcome(ctx, db, ident, cfg, req, domain.SearchResponse{
		SchemaVersion: 1,
		Results:       []domain.SearchResult{{SampleID: "sha256:abc", Grade: domain.GradeExact}},
	})
	after, _ := db.QueuePending(ctx, 10)
	for _, it := range after[before:] {
		if it.Kind == evidence.WantedCandidateQueueKind {
			t.Error("an answered question was recorded as wanted")
		}
	}
}

func TestJavaMavenMissQueuesWantedWithoutClaimingAdapterSupport(t *testing.T) {
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity

	req := domain.SearchRequest{
		SchemaVersion: 2,
		Packages:      []string{"pkg:maven/org.apache.commons/commons-lang3@3.17.0"},
		Symbols:       []string{"StringUtils.isBlank"},
	}
	recordSearchOutcome(t.Context(), db, ident, cfg, req,
		domain.SearchResponse{SchemaVersion: 2, Miss: true})

	items, err := db.QueuePending(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != evidence.WantedCandidateQueueKind ||
		!strings.Contains(items[0].Payload, "pkg:maven/org.apache.commons/commons-lang3@3.17.0") {
		t.Fatalf("Maven miss did not become a wanted candidate: %+v", items)
	}
	if !domain.AllowedEcosystems["maven"] {
		t.Fatal("verified Maven support must be in the public ecosystem allowlist")
	}
}

func TestEngineOnlyMissQueuesFixedPublicTargetAndDropsPrivateFramework(t *testing.T) {
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity

	req := domain.SearchRequest{
		SchemaVersion: 2,
		Symbols:       []string{"AssetDatabase.Refresh"},
		Environment: domain.EnvironmentFingerprint{Frameworks: []string{
			"unity@6000.0.24f1", "company-secret-sdk@7.2.0",
		}},
	}
	recordSearchOutcome(t.Context(), db, ident, cfg, req,
		domain.SearchResponse{SchemaVersion: 2, Miss: true})

	items, err := db.QueuePending(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.Contains(items[0].Payload, "pkg:generic/engine/unity@6000.0.24f1") {
		t.Fatalf("Unity target was not queued: %+v", items)
	}
	if strings.Contains(items[0].Payload, "company-secret") {
		t.Fatalf("arbitrary framework name leaked into Wanted: %s", items[0].Payload)
	}
	if domain.AllowedEcosystems["generic"] {
		t.Fatal("wanted targets must not become automatic evidence ecosystems")
	}
}

// Local-only mode reports nothing, ever.
func TestLocalOnlyRecordsNoWant(t *testing.T) {
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	recordSearchOutcome(t.Context(), db, ident, cfg,
		domain.SearchRequest{SchemaVersion: 1, Packages: []string{"pkg:npm/dotenv@17.2.3"}},
		domain.SearchResponse{SchemaVersion: 1, Miss: true})
	items, _ := db.QueuePending(t.Context(), 10)
	for _, it := range items {
		if it.Kind == evidence.WantedCandidateQueueKind {
			t.Error("local-only mode queued a report")
		}
	}
}
