package search

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func openDB(t *testing.T) *localdb.DB {
	t.Helper()
	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func nodeEnv(module string) domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", OSVersionBucket: "11",
		Arch: "amd64", Runtime: "node", RuntimeVersion: "22.18",
		Language: "typescript", LanguageVersion: "5.9",
		ModuleSystem: module, ExecutionContext: "node",
	}
}

func mkManifest(goal string, pkgs []string, env domain.EnvironmentFingerprint, symbols ...string) domain.SampleManifest {
	return domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW", Goal: goal,
			Packages: pkgs, Symbols: symbols,
			Contract: []string{"asserts documented behavior"},
		},
		Packages: pkgs, Symbols: symbols, Environment: env,
		License:         "MIT-0",
		ContractCommand: []string{"node", "test/contract.mjs"},
		VerifierAdapter: "node-typescript@1",
	}
}

func hasEntry(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func hasEntryWithPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func saveShardJSON(t *testing.T, db *localdb.DB, key string, sf shardFile) {
	t.Helper()
	b, err := json.Marshal(sf)
	if err != nil {
		t.Fatalf("marshal shard: %v", err)
	}
	if err := db.SaveShard(context.Background(), key, "etag-"+key, string(b)); err != nil {
		t.Fatalf("save shard: %v", err)
	}
}

func saveResolvedReceipt(t *testing.T, db *localdb.DB, sampleID string, m domain.SampleManifest, peer string) {
	t.Helper()
	caseID := m.Case.CaseID
	if caseID == "" {
		caseID = m.Case.ComputeID()
	}
	receipt := domain.VerificationReceipt{
		SchemaVersion: 2, SampleID: sampleID, CaseID: caseID,
		EnvironmentHash: m.Environment.Normalize().Hash(), Environment: m.Environment,
		Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
		ResolvedPackages: m.Packages, VerifierAdapter: m.VerifierAdapter,
		SandboxCapability: domain.CapContainerRun, LogsDigest: "sha256:test",
		CreatedAt: time.Now().UTC().Format(time.RFC3339), PeerID: peer,
		PeerPubkey: "test", PeerSignature: "test",
	}
	if err := db.SaveReceipt(context.Background(), receipt); err != nil {
		t.Fatalf("save resolved receipt for %s: %v", sampleID, err)
	}
}

// ESM sample answering a CJS request must come back ADAPTATION_REQUIRED
// with the §11.5-style import-syntax delta, never EXACT/COMPATIBLE.
func TestSearchESMSampleCJSRequestAdaptationRequired(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := mkManifest("upload multipart form with axios",
		[]string{"pkg:npm/axios@1.12.0"}, nodeEnv("esm"), "axios.post")
	if err := SeedSampleDoc(ctx, db, m, "sha256:aaa1", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	saveResolvedReceipt(t, db, "sha256:aaa1", m, "ed25519:aaaa111122223333")

	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "upload multipart form with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   nodeEnv("cjs"),
	})
	if resp.Miss || len(resp.Results) == 0 {
		t.Fatalf("expected hit, got miss=%v results=%d", resp.Miss, len(resp.Results))
	}
	r := resp.Results[0]
	if r.Grade != domain.GradeAdaptationRequired {
		t.Fatalf("grade = %s, want ADAPTATION_REQUIRED", r.Grade)
	}
	if !hasEntry(r.Adaptation, "Import syntax only") {
		t.Errorf("adaptation missing 'Import syntax only': %v", r.Adaptation)
	}
	if !hasEntry(r.Different, "Sample uses ESM") || !hasEntry(r.Different, "Current project uses CJS") {
		t.Errorf("different missing module-system pair: %v", r.Different)
	}
	if !hasEntry(r.Exact, "axios 1.12") {
		t.Errorf("exact missing package entry: %v", r.Exact)
	}
	if !hasEntry(r.Exact, "node 22") {
		t.Errorf("exact missing runtime entry: %v", r.Exact)
	}
	if r.SampleID != "sha256:aaa1" || r.SampleStatus != "LOCAL_PASS" {
		t.Errorf("sample id/status = %q/%q", r.SampleID, r.SampleStatus)
	}
	if r.Case == nil || r.Case.Goal == "" {
		t.Errorf("case not attached: %+v", r.Case)
	}
}

// executionContext is ALWAYS sensitive (docs/execution-context.md §5): a
// node-context sample answering a safari request is capped at
// ADAPTATION_REQUIRED with a "verify in <ctx>" adaptation entry.
func TestSearchBrowserContextMismatchCapsGrade(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := mkManifest("fetch json with axios",
		[]string{"pkg:npm/axios@1.12.0"}, nodeEnv("esm"), "axios.get")
	if err := SeedSampleDoc(ctx, db, m, "sha256:bbb1", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "fetch json with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "macos", Arch: "arm64",
			ModuleSystem: "esm", BrowserFamily: "safari", BrowserMajor: "19",
		},
	})
	if resp.Miss || len(resp.Results) == 0 {
		t.Fatalf("expected hit, got miss=%v", resp.Miss)
	}
	r := resp.Results[0]
	if r.Grade != domain.GradeAdaptationRequired {
		t.Fatalf("grade = %s, want ADAPTATION_REQUIRED (context mismatch cap)", r.Grade)
	}
	if !hasEntryWithPrefix(r.Adaptation, "verify in safari") {
		t.Errorf("adaptation missing 'verify in safari...': %v", r.Adaptation)
	}
}

// An elevated failure cluster matching the requester's environment demotes
// the result to REFERENCE_ONLY and surfaces the cluster (hypotheses passed
// through, never asserted as definitive).
func TestSearchElevatedFailureInRequesterEnvReferenceOnly(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := mkManifest("upload multipart form with axios",
		[]string{"pkg:npm/axios@1.12.0"}, nodeEnv("cjs"), "axios.post")
	if err := SeedSampleDoc(ctx, db, m, "sha256:ccc1", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	saveShardJSON(t, db, "npm/axios/1", shardFile{
		SchemaVersion: 1, Key: "npm/axios/1", GeneratedAt: now,
		Packages: []shardPackage{{
			PURL: "pkg:npm/axios@1.12.0",
			Symbols: []shardSymbolEntry{{
				Family: "axios.post",
				Stats: shardSymbolStats{
					ObservationCount: 40, UniquePeerBuckets: 4, PassRate: 0.9,
					ByStage:    map[string]shardStageCount{"PROJECT_COMPILE": {Pass: 30, Fail: 4}},
					Confidence: "HIGH", LastSeen: now,
				},
				Failures: []shardFailure{{
					ErrorCode: "ERR_REQUIRE_ESM", Fingerprint: "sha256:feedfeed", Count: 12,
					EnvSummary: map[string]string{"moduleSystem": "cjs", "runtime": "node@22"},
					Hypotheses: []domain.FailureHypothesis{
						{Domain: domain.FailConfiguration, Confidence: 0.7},
						{Domain: domain.FailUnknown, Confidence: 0.3},
					},
				}},
			}},
		}},
	})

	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "upload multipart form with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   nodeEnv("cjs"),
	})
	if resp.Miss || len(resp.Results) == 0 {
		t.Fatalf("expected demoted hit, got miss=%v", resp.Miss)
	}
	r := resp.Results[0]
	if r.Grade != domain.GradeReferenceOnly {
		t.Fatalf("grade = %s, want REFERENCE_ONLY (elevated failure in requester env)", r.Grade)
	}
	if len(r.KnownFailures) != 1 {
		t.Fatalf("known failures = %d, want 1", len(r.KnownFailures))
	}
	kf := r.KnownFailures[0]
	if kf.ErrorCode != "ERR_REQUIRE_ESM" || kf.Count != 12 || len(kf.Hypotheses) != 2 {
		t.Errorf("known failure not passed through: %+v", kf)
	}
	// PROJECT_* observation counts and contract evidence stay separate.
	if r.Evidence.ProjectCompileObservations != 34 {
		t.Errorf("projectCompileObservations = %d, want 34", r.Evidence.ProjectCompileObservations)
	}
	if r.Evidence.CleanBuilds != 30 {
		t.Errorf("cleanBuilds = %d, want 30", r.Evidence.CleanBuilds)
	}
	if r.Evidence.ContractPasses != 0 {
		t.Errorf("contractPasses = %d, want 0 (no receipts)", r.Evidence.ContractPasses)
	}
	if r.Evidence.UniquePeerBuckets != 4 {
		t.Errorf("uniquePeerBuckets = %d, want 4", r.Evidence.UniquePeerBuckets)
	}
	if len(r.Evidence.ElevatedFailures) == 0 {
		t.Errorf("elevated failures not surfaced in evidence summary")
	}
}

// An exact error-fingerprint hit in a shard failure list outranks a plain
// package+FTS match (§11.3 step 3 before step 4), and searching FOR the
// failure does not demote its fix to REFERENCE_ONLY.
func TestSearchErrorFingerprintRanksFirst(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	const fp = "sha256:deadbeef"
	mA := mkManifest("fix require esm error when loading axios",
		[]string{"pkg:npm/axios@1.12.0"}, nodeEnv("esm"), "axios.get")
	if err := SeedSampleDoc(ctx, db, mA, "sha256:fixa", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	mB := mkManifest("clone object deeply with lodash",
		[]string{"pkg:npm/lodash@4.17.21"}, nodeEnv("cjs"), "lodash.cloneDeep")
	if err := SeedSampleDoc(ctx, db, mB, "sha256:lodb", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	saveShardJSON(t, db, "npm/axios/1", shardFile{
		SchemaVersion: 1, Key: "npm/axios/1",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Packages: []shardPackage{{
			PURL: "pkg:npm/axios@1.12.0",
			Symbols: []shardSymbolEntry{{
				Family: "axios.get",
				Stats:  shardSymbolStats{ObservationCount: 10, UniquePeerBuckets: 2, PassRate: 0.8},
				Failures: []shardFailure{{
					ErrorCode: "ERR_REQUIRE_ESM", Fingerprint: fp, Count: 7,
					EnvSummary: map[string]string{"moduleSystem": "esm"},
				}},
			}},
		}},
	})

	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion:    1,
		Query:            "require esm error",
		Packages:         []string{"pkg:npm/axios@1.12.0", "pkg:npm/lodash@4.17.21"},
		Environment:      nodeEnv("cjs"),
		ErrorFingerprint: fp,
		ErrorCode:        "ERR_REQUIRE_ESM",
	})
	if resp.Miss || len(resp.Results) == 0 {
		t.Fatalf("expected a result, got miss=%v n=%d", resp.Miss, len(resp.Results))
	}
	if resp.Results[0].SampleID != "sha256:fixa" {
		t.Fatalf("results[0] = %s, want fingerprint-matched sample sha256:fixa", resp.Results[0].SampleID)
	}
	if resp.Results[0].Grade == domain.GradeReferenceOnly {
		t.Errorf("fix for the searched failure must not be demoted to REFERENCE_ONLY")
	}
	// The lodash cloneDeep sample shares a package with the request and
	// nothing else — it is not an answer to "require esm error", and
	// offering it as an alternative is the wrong-HIT this project rates
	// worse than a miss.
	for _, r := range resp.Results {
		if r.SampleID == "sha256:lodb" {
			t.Errorf("unrelated same-package sample returned as an alternative (score %f)", r.Score)
		}
	}
}

// A query matching nothing yields NO_SAFE_MATCH: Miss=true, no results.
func TestSearchNonsenseQueryMiss(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := mkManifest("upload multipart form with axios",
		[]string{"pkg:npm/axios@1.12.0"}, nodeEnv("esm"), "axios.post")
	if err := SeedSampleDoc(ctx, db, m, "sha256:ddd1", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "quantum blockchain teapot",
		Environment:   nodeEnv("cjs"),
	})
	if !resp.Miss {
		t.Fatalf("expected Miss=true for nonsense query, got results=%d", len(resp.Results))
	}
	if len(resp.Results) != 0 {
		t.Fatalf("Miss response must carry no results, got %d", len(resp.Results))
	}
}

// CROSS_PASS verification (L4, independent peers) outranks LOCAL_PASS (L3)
// for otherwise equivalent samples; receipts feed the evidence summary.
func TestSearchCrossPassOutranksLocalPass(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	env := nodeEnv("cjs")
	mC := mkManifest("post json with axios client",
		[]string{"pkg:npm/axios@1.12.0"}, env, "axios.post")
	if err := SeedSampleDoc(ctx, db, mC, "sha256:crossc", "CROSS_PASS"); err != nil {
		t.Fatalf("seed C: %v", err)
	}
	mL := mkManifest("post json using axios client",
		[]string{"pkg:npm/axios@1.12.0"}, env, "axios.post")
	if err := SeedSampleDoc(ctx, db, mL, "sha256:locall", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed L: %v", err)
	}
	mkReceipt := func(peer string) domain.VerificationReceipt {
		return domain.VerificationReceipt{
			SchemaVersion: 2, SampleID: "sha256:crossc", CaseID: mC.Case.ComputeID(),
			EnvironmentHash: env.Hash(), Environment: env,
			Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
			ResolvedPackages: mC.Packages,
			VerifierAdapter:  "node-typescript@1", SandboxCapability: domain.CapContainerRun,
			LogsDigest: "sha256:log", CreatedAt: time.Now().UTC().Format(time.RFC3339),
			PeerID: peer, PeerPubkey: "pk", PeerSignature: "sig",
		}
	}
	if err := db.SaveReceipt(ctx, mkReceipt("ed25519:aaaa111122223333")); err != nil {
		t.Fatalf("receipt 1: %v", err)
	}
	if err := db.SaveReceipt(ctx, mkReceipt("ed25519:bbbb111122223333")); err != nil {
		t.Fatalf("receipt 2: %v", err)
	}

	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "post json axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   env,
	})
	if resp.Miss || len(resp.Results) < 2 {
		t.Fatalf("expected 2 results, got miss=%v n=%d", resp.Miss, len(resp.Results))
	}
	if resp.Results[0].SampleID != "sha256:crossc" {
		t.Fatalf("results[0] = %s, want CROSS_PASS sample first", resp.Results[0].SampleID)
	}
	if resp.Results[0].Score <= resp.Results[1].Score {
		t.Errorf("CROSS_PASS should outscore LOCAL_PASS: %f <= %f",
			resp.Results[0].Score, resp.Results[1].Score)
	}
	ev := resp.Results[0].Evidence
	if ev.ContractPasses != 2 {
		t.Errorf("contractPasses = %d, want 2", ev.ContractPasses)
	}
	if ev.IndependentCrossPeers != 2 {
		t.Errorf("independentCrossPeers = %d, want 2", ev.IndependentCrossPeers)
	}
	if resp.Results[0].Grade != domain.GradeExact {
		t.Errorf("grade = %s, want EXACT (same major.minor, all sensitive dims equal)", resp.Results[0].Grade)
	}
}

// The default limit is 3 even when more candidates clear the threshold.
func TestSearchDefaultLimit(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	env := nodeEnv("cjs")
	ids := []string{"sha256:l1", "sha256:l2", "sha256:l3", "sha256:l4"}
	goals := []string{
		"post json with axios one", "post json with axios two",
		"post json with axios three", "post json with axios four",
	}
	for i, id := range ids {
		m := mkManifest(goals[i], []string{"pkg:npm/axios@1.12.0"}, env, "axios.post")
		if err := SeedSampleDoc(ctx, db, m, id, "LOCAL_PASS"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1, Query: "post json axios",
		Packages:    []string{"pkg:npm/axios@1.12.0"},
		Environment: env,
	})
	if resp.Miss {
		t.Fatal("unexpected miss")
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %d, want default limit 3", len(resp.Results))
	}
}

// TestPackageMatchAloneIsNotAnAnswer pins the wrong-HIT this project rates
// worse than a miss (goal.md §3.8). An exact package match scores 0.45
// before any multiplier — past missThreshold on its own — so every
// verified sample for a package in the caller's dependency tree used to
// come back as a confident hit no matter what was asked. Reproduced on
// production: "how to bake a chocolate cake" with google/uuid in go.mod
// returned the UUID sample as MATCH: EXACT.
func TestPackageMatchAloneIsNotAnAnswer(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := mkManifest("Generate, parse and validate a UUID in Go with google/uuid",
		[]string{"pkg:npm/axios@1.12.0"}, nodeEnv("esm"), "uuid.New")
	// MATRIX_PASS is the strongest verification level, so this candidate
	// gets the largest strength multiplier — the worst case for the gate.
	if err := SeedSampleDoc(ctx, db, m, "sha256:uuid1", "MATRIX_PASS"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The package IS in the caller's tree; the question is unrelated.
	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "how to bake a chocolate cake",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   nodeEnv("esm"),
	})
	if !resp.Miss || len(resp.Results) != 0 {
		var got string
		if len(resp.Results) > 0 {
			got = fmt.Sprintf(" (%s, grade %s, score %f)",
				resp.Results[0].SampleID, resp.Results[0].Grade, resp.Results[0].Score)
		}
		t.Fatalf("unrelated query returned a hit on package overlap alone%s", got)
	}

	// The same sample must still be found by a question it actually answers.
	on := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "validate a uuid in go",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   nodeEnv("esm"),
	})
	if on.Miss || len(on.Results) == 0 {
		t.Fatal("the relevance floor also blocked an on-topic query")
	}
	if on.Results[0].SampleID != "sha256:uuid1" {
		t.Fatalf("on-topic results[0] = %s", on.Results[0].SampleID)
	}
}

// TestRelevanceGateAcceptsDifferentWording: the gate exists to reject
// questions a sample has nothing to do with, not questions that name the
// same subject differently. "render a react component to an html string"
// shares no content word with the goal "Choose between renderToString and
// renderToStaticMarkup without breaking hydration" — the word in common is
// the package name, and rejecting that lost a correct answer.
func TestRelevanceGateAcceptsDifferentWording(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := mkManifest("Choose between renderToString and renderToStaticMarkup without breaking hydration",
		[]string{"pkg:npm/react@19.2.8"}, nodeEnv("esm"), "renderToStaticMarkup")
	if err := SeedSampleDoc(ctx, db, m, "sha256:react1", "CROSS_PASS"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	on := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "render a react component to an html string",
		Packages:      []string{"pkg:npm/react@19.2.8"},
		Environment:   nodeEnv("esm"),
	})
	if on.Miss || len(on.Results) == 0 {
		t.Fatal("a question naming the package instead of the API must still find the sample")
	}

	// The gate still closes on a genuinely unrelated question.
	off := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "how to bake a chocolate cake",
		Packages:      []string{"pkg:npm/react@19.2.8"},
		Environment:   nodeEnv("esm"),
	})
	if !off.Miss || len(off.Results) != 0 {
		t.Errorf("unrelated question still hit: %+v", off.Results)
	}
}

// TestPackageNameTokensSplitOnPunctuation: a sample listed in several
// shards used to keep only the first package it was seen under, and a
// hyphenated name was one token. Together those made "render a react
// component" miss its own sample, because the candidate had been reduced
// to react-dom and "react-dom" is not "react".
func TestPackageNameTokensSplitOnPunctuation(t *testing.T) {
	got := contentTokens("react-dom @scope/pkg tailwind.config.js")
	want := map[string]bool{"react": true, "dom": true, "scope": true,
		"pkg": true, "tailwind": true, "config": true}
	for w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("token %q missing from %v", w, got)
		}
	}
}

func TestCandidateAccumulatesPackagesAcrossShards(t *testing.T) {
	base := []domain.PURL{{Ecosystem: "npm", Name: "react-dom", Version: "19.2.8"}}
	added := appendPURL(base, domain.PURL{Ecosystem: "npm", Name: "react", Version: "19.2.8"})
	if len(added) != 2 {
		t.Fatalf("packages = %v, want both", added)
	}
	// Idempotent: the same purl seen twice does not duplicate.
	again := appendPURL(added, domain.PURL{Ecosystem: "npm", Name: "React", Version: "19.2.8"})
	if len(again) != 2 {
		t.Errorf("duplicate purl added: %v", again)
	}
}
