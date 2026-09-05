package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
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

// A reporter who hit the problem on Windows is asking for a Windows answer.
// The server stores target_os, the queue pins WANTED work on it, and the
// /wanted page renders it — but none of that runs unless the miss RECORDS the
// platform it happened on, and this is the only producer.
func TestAMissCarriesTheOSItHappenedOn(t *testing.T) {
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
		Packages:      []string{"pkg:npm/dotenv@17.2.3"},
		Environment:   domain.EnvironmentFingerprint{OS: "Windows"},
	}
	recordSearchOutcome(ctx, db, ident, cfg, req, domain.SearchResponse{SchemaVersion: 1, Miss: true})

	items, err := db.QueuePending(ctx, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v err=%v", items, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(items[0].Payload), &m); err != nil {
		t.Fatal(err)
	}
	if m["os"] != "windows" {
		t.Errorf("os = %v, want the platform the miss happened on, normalized", m["os"])
	}

	// No recorded OS stays no OS: a question about the package, answerable by
	// anyone who can run it.
	req.Packages = []string{"pkg:npm/ms@2.1.3"}
	req.Environment = domain.EnvironmentFingerprint{}
	recordSearchOutcome(ctx, db, ident, cfg, req, domain.SearchResponse{SchemaVersion: 1, Miss: true})
	items, _ = db.QueuePending(ctx, 10)
	found := false
	for _, it := range items {
		if !strings.Contains(it.Payload, "pkg:npm/ms@2.1.3") {
			continue
		}
		found = true
		// A fresh map: Unmarshal merges into an existing one and would
		// carry the first report's os over.
		var second map[string]any
		if err := json.Unmarshal([]byte(it.Payload), &second); err != nil {
			t.Fatal(err)
		}
		if _, present := second["os"]; present {
			t.Errorf("an unreported OS was invented: %v", second["os"])
		}
	}
	if !found {
		t.Fatal("the second miss recorded nothing")
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

func TestExplicitCLITargetMissQueuesWantedWithoutBecomingEvidenceEcosystem(t *testing.T) {
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
		Packages:      []string{"pkg:generic/cli/pnpm@10.15.0"},
		Symbols:       []string{"pnpm deploy"},
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "generic", OS: "windows", OSVersionBucket: "11", Arch: "x64",
			Runtime: "pnpm", RuntimeVersion: "10.15.0",
		},
	}
	recordSearchOutcome(t.Context(), db, ident, cfg, req,
		domain.SearchResponse{SchemaVersion: 2, Miss: true})

	items, err := db.QueuePending(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != evidence.WantedCandidateQueueKind ||
		!strings.Contains(items[0].Payload, "pkg:generic/cli/pnpm@10.15.0") ||
		!strings.Contains(items[0].Payload, "pnpm deploy") {
		t.Fatalf("CLI miss did not become a privacy-reduced Wanted candidate: %+v", items)
	}
	if domain.AllowedEcosystems["generic"] {
		t.Fatal("generic CLI targets must not become unverified automatic evidence")
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

func TestUnrealDemandRecordedViaMCPSearchTool(t *testing.T) {
	unrealDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrealDir, "MyGame.uproject"),
		[]byte(`{"EngineAssociation":"5.5"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	db, err := localdb.Open(filepath.Join(stateDir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity

	deps := emptyDeps()
	deps.MachineEnv = func(ctx context.Context) domain.EnvironmentFingerprint {
		return environment.Collect(ctx, projectEnvironmentHints(unrealDir))
	}
	deps.SearchRaw = func(ctx context.Context, req domain.SearchRequest) domain.SearchResponse {
		return domain.SearchResponse{SchemaVersion: 2, Miss: true}
	}
	deps.RecordSearchOutcome = func(ctx context.Context, req domain.SearchRequest, resp domain.SearchResponse) string {
		return recordSearchOutcome(ctx, db, ident, cfg, req, resp)
	}

	c := startServer(t, deps)
	res := callTool(t, c, "search_known_solution", map[string]any{
		"query": "nanite material blending in c++",
	})
	if res["isError"] != nil && res["isError"].(bool) {
		t.Fatalf("search_known_solution error: %v", toolText(t, res))
	}

	items, err := db.QueuePending(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range items {
		if it.Kind != evidence.WantedCandidateQueueKind {
			continue
		}
		found = true
		var report struct {
			Packages []string `json:"packages"`
			Symbols  []string `json:"symbols"`
		}
		if err := json.Unmarshal([]byte(it.Payload), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Packages) != 1 || report.Packages[0] != "pkg:generic/engine/unreal@5.5" {
			t.Fatalf("queued packages = %v, want exactly [pkg:generic/engine/unreal@5.5]", report.Packages)
		}
	}
	if !found {
		t.Fatal("expected Unreal wanted candidate to be queued from MCP search")
	}
}

func TestUnrealDemandWithSymbolAttributionViaMCPSearch(t *testing.T) {
	unrealDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrealDir, "MyGame.uproject"),
		[]byte(`{"EngineAssociation":"5.5"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	db, err := localdb.Open(filepath.Join(stateDir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity

	deps := emptyDeps()
	deps.MachineEnv = func(ctx context.Context) domain.EnvironmentFingerprint {
		return environment.Collect(ctx, projectEnvironmentHints(unrealDir))
	}
	deps.SearchRaw = func(ctx context.Context, req domain.SearchRequest) domain.SearchResponse {
		return domain.SearchResponse{SchemaVersion: 2, Miss: true}
	}
	deps.RecordSearchOutcome = func(ctx context.Context, req domain.SearchRequest, resp domain.SearchResponse) string {
		return recordSearchOutcome(ctx, db, ident, cfg, req, resp)
	}

	c := startServer(t, deps)
	res := callTool(t, c, "search_known_solution", map[string]any{
		"query":   "attach mesh component",
		"symbols": []string{"UStaticMeshComponent.SetStaticMesh"},
	})
	if res["isError"] != nil && res["isError"].(bool) {
		t.Fatalf("search_known_solution error: %v", toolText(t, res))
	}

	items, err := db.QueuePending(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range items {
		if it.Kind != evidence.WantedCandidateQueueKind {
			continue
		}
		found = true
		var report struct {
			Packages []string `json:"packages"`
			Symbols  []string `json:"symbols"`
		}
		if err := json.Unmarshal([]byte(it.Payload), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Packages) != 1 || report.Packages[0] != "pkg:generic/engine/unreal@5.5" {
			t.Fatalf("queued packages = %v, want [pkg:generic/engine/unreal@5.5]", report.Packages)
		}
		if len(report.Symbols) != 1 || report.Symbols[0] != "UStaticMeshComponent.SetStaticMesh" {
			t.Fatalf("queued symbols = %v, want [UStaticMeshComponent.SetStaticMesh]", report.Symbols)
		}
	}
	if !found {
		t.Fatal("expected Unreal wanted candidate with symbols to be queued")
	}
}

func TestUnrelatedPackageSearchDoesNotFabricateFrameworkDemandViaMCP(t *testing.T) {
	unrealDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrealDir, "MyGame.uproject"),
		[]byte(`{"EngineAssociation":"5.5"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	db, err := localdb.Open(filepath.Join(stateDir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity

	deps := emptyDeps()
	deps.MachineEnv = func(ctx context.Context) domain.EnvironmentFingerprint {
		return environment.Collect(ctx, projectEnvironmentHints(unrealDir))
	}
	deps.SearchRaw = func(ctx context.Context, req domain.SearchRequest) domain.SearchResponse {
		return domain.SearchResponse{SchemaVersion: 2, Miss: true}
	}
	deps.RecordSearchOutcome = func(ctx context.Context, req domain.SearchRequest, resp domain.SearchResponse) string {
		return recordSearchOutcome(ctx, db, ident, cfg, req, resp)
	}

	c := startServer(t, deps)
	res := callTool(t, c, "search_known_solution", map[string]any{
		"query":    "axios post request",
		"packages": []string{"pkg:npm/axios@1.12.0"},
	})
	if res["isError"] != nil && res["isError"].(bool) {
		t.Fatalf("search_known_solution error: %v", toolText(t, res))
	}

	items, err := db.QueuePending(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var foundAxios bool
	for _, it := range items {
		if it.Kind != evidence.WantedCandidateQueueKind {
			continue
		}
		if strings.Contains(it.Payload, "unreal") {
			t.Fatalf("Unreal framework demand fabricated in axios package search: %s", it.Payload)
		}
		if strings.Contains(it.Payload, "pkg:npm/axios@1.12.0") {
			foundAxios = true
		}
	}
	if !foundAxios {
		t.Fatal("expected axios to be queued in package search")
	}
}

func TestUnrelatedEcosystemSearchDoesNotFabricateFrameworkDemandViaMCP(t *testing.T) {
	unrealDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrealDir, "MyGame.uproject"),
		[]byte(`{"EngineAssociation":"5.5"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	db, err := localdb.Open(filepath.Join(stateDir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity

	deps := emptyDeps()
	deps.MachineEnv = func(ctx context.Context) domain.EnvironmentFingerprint {
		return environment.Collect(ctx, projectEnvironmentHints(unrealDir))
	}
	deps.SearchRaw = func(ctx context.Context, req domain.SearchRequest) domain.SearchResponse {
		return domain.SearchResponse{SchemaVersion: 2, Miss: true}
	}
	deps.RecordSearchOutcome = func(ctx context.Context, req domain.SearchRequest, resp domain.SearchResponse) string {
		return recordSearchOutcome(ctx, db, ident, cfg, req, resp)
	}

	c := startServer(t, deps)
	res := callTool(t, c, "search_known_solution", map[string]any{
		"query": "express route handling",
		"environment": map[string]any{
			"ecosystem": "npm",
		},
	})
	if res["isError"] != nil && res["isError"].(bool) {
		t.Fatalf("search_known_solution error: %v", toolText(t, res))
	}

	items, err := db.QueuePending(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Kind == evidence.WantedCandidateQueueKind {
			t.Fatalf("wanted report queued for ecosystem search with no packages: %s", it.Payload)
		}
	}
}

func TestUnrealCommandFailureLookupQueuesWantViaMCP(t *testing.T) {
	unrealDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrealDir, "MyGame.uproject"),
		[]byte(`{"EngineAssociation":"5.5"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	db, err := localdb.Open(filepath.Join(stateDir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity

	deps := emptyDeps()
	deps.MachineEnv = func(ctx context.Context) domain.EnvironmentFingerprint {
		return environment.Collect(ctx, projectEnvironmentHints(unrealDir))
	}
	deps.RunObserved = func(ctx context.Context, argv []string, cwd string) (int, string, string, []string, evidence.CommandOutput, error) {
		return 1, "PROJECT_COMPILE", "FAIL", []string{"errorCode: UE-1001", "unreal compilation failed"}, evidence.CommandOutput{Stderr: "error"}, nil
	}
	deps.Search = func(ctx context.Context, req domain.SearchRequest) (domain.SearchResponse, string) {
		resp := domain.SearchResponse{SchemaVersion: 2, Miss: true}
		return resp, recordSearchOutcome(ctx, db, ident, cfg, req, resp)
	}

	c := startServer(t, deps)
	res := callTool(t, c, "run_observed_command", map[string]any{
		"command": []string{"cmd", "/c", "Build.bat"},
		"cwd":     unrealDir,
	})
	if res["isError"] != nil && res["isError"].(bool) {
		t.Fatalf("run_observed_command error: %v", toolText(t, res))
	}

	items, err := db.QueuePending(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var foundUnreal bool
	for _, it := range items {
		if it.Kind != evidence.WantedCandidateQueueKind {
			continue
		}
		if strings.Contains(it.Payload, "pkg:generic/engine/unreal@5.5") {
			foundUnreal = true
		}
	}
	if !foundUnreal {
		t.Fatal("expected Unreal wanted candidate to be queued from failure auto-lookup")
	}
}

func TestUnrelatedCommandFailureDoesNotFabricateFrameworkDemandViaMCP(t *testing.T) {
	unrealDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrealDir, "MyGame.uproject"),
		[]byte(`{"EngineAssociation":"5.5"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	db, err := localdb.Open(filepath.Join(stateDir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity

	deps := emptyDeps()
	deps.MachineEnv = func(ctx context.Context) domain.EnvironmentFingerprint {
		return environment.Collect(ctx, projectEnvironmentHints(unrealDir))
	}
	deps.RunObserved = func(ctx context.Context, argv []string, cwd string) (int, string, string, []string, evidence.CommandOutput, error) {
		return 1, "PROJECT_TEST", "FAIL", []string{"errorCode: TS1000", "npm test failed"}, evidence.CommandOutput{Stderr: "error"}, nil
	}
	deps.Search = func(ctx context.Context, req domain.SearchRequest) (domain.SearchResponse, string) {
		resp := domain.SearchResponse{SchemaVersion: 2, Miss: true}
		return resp, recordSearchOutcome(ctx, db, ident, cfg, req, resp)
	}

	c := startServer(t, deps)
	res := callTool(t, c, "run_observed_command", map[string]any{
		"command": []string{"npm", "test"},
		"cwd":     unrealDir,
	})
	if res["isError"] != nil && res["isError"].(bool) {
		t.Fatalf("run_observed_command error: %v", toolText(t, res))
	}

	items, err := db.QueuePending(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Kind == evidence.WantedCandidateQueueKind && strings.Contains(it.Payload, "unreal") {
			t.Fatalf("npm test failure fabricated Unreal framework demand: %s", it.Payload)
		}
	}
}

