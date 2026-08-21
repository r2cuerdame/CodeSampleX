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

func saveReceiptWithPackages(t *testing.T, db *localdb.DB, sampleID string, m domain.SampleManifest,
	schemaVersion int, resolved []string, contractResult string) {
	t.Helper()
	caseID := m.Case.CaseID
	if caseID == "" {
		caseID = m.Case.ComputeID()
	}
	receipt := domain.VerificationReceipt{
		SchemaVersion: schemaVersion, SampleID: sampleID, CaseID: caseID,
		EnvironmentHash: m.Environment.Normalize().Hash(), Environment: m.Environment,
		Stages: map[string]string{
			"resolve":  string(domain.ResultPass),
			"compile":  string(domain.ResultPass),
			"contract": contractResult,
		},
		ResolvedPackages: resolved, VerifierAdapter: m.VerifierAdapter,
		SandboxCapability: domain.CapContainerRun, LogsDigest: "sha256:test",
		CreatedAt: time.Now().UTC().Format(time.RFC3339), PeerID: "ed25519:aaaa111122223333",
		PeerPubkey: "test", PeerSignature: "test",
	}
	if err := db.SaveReceipt(context.Background(), receipt); err != nil {
		t.Fatalf("save receipt for %s: %v", sampleID, err)
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
	saveResolvedReceipt(t, db, "sha256:fixa", mA, "ed25519:aaaa111122223333")
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
	if !resp.Results[0].ExactFailureMatched {
		t.Error("exact fingerprint match was not exposed on SearchResult")
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

	// The same error CODE is useful for relevance but is not an exact
	// fingerprint match. Neither is a semantic/package hit with no error.
	for _, tc := range []struct {
		name string
		req  domain.SearchRequest
	}{
		{
			name: "error code only",
			req: domain.SearchRequest{
				SchemaVersion: 1, Query: "require esm error",
				Packages: []string{"pkg:npm/axios@1.12.0"}, Environment: nodeEnv("cjs"),
				ErrorFingerprint: "sha256:not-the-recorded-fingerprint", ErrorCode: "ERR_REQUIRE_ESM",
			},
		},
		{
			name: "semantic package hit",
			req: domain.SearchRequest{
				SchemaVersion: 1, Query: "fix require esm error when loading axios",
				Packages: []string{"pkg:npm/axios@1.12.0"}, Environment: nodeEnv("cjs"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (Engine{DB: db}).Search(ctx, tc.req)
			if got.Miss || len(got.Results) == 0 {
				t.Fatalf("expected relevance hit, got miss=%v", got.Miss)
			}
			if got.Results[0].ExactFailureMatched {
				t.Error("nonmatching fingerprint was promoted to an exact failure match")
			}
		})
	}
}

func TestExactFailureMatchedRequiresDeclaredSymbolAndSelectedPassContract(t *testing.T) {
	const fp = "sha256:exact-failure"
	tests := []struct {
		name             string
		candidateSym     string
		clusterPackage   string
		clusterSym       string
		contract         []string
		packages         []string
		requestPackages  []string
		omitPackages     bool
		receiptSchema    int
		resolvedPackages []string
		contractResult   string
		wantExact        bool
	}{
		{
			name: "same package symbol resolved pass", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"returns parsed JSON"}, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/axios@1.12.0"}, contractResult: "PASS", wantExact: true,
		},
		{
			name: "pass resolves different package", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"returns parsed JSON"},
			packages: []string{"pkg:npm/axios@1.12.0", "pkg:npm/lodash@4.17.21"}, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/lodash@4.17.21"}, contractResult: "PASS", wantExact: false,
		},
		{
			name: "explicit axios excludes lodash failure", candidateSym: "lodash.get",
			clusterPackage: "lodash", clusterSym: "lodash.get",
			contract:        []string{"returns parsed JSON"},
			packages:        []string{"pkg:npm/axios@1.12.0", "pkg:npm/lodash@4.17.21"},
			requestPackages: []string{"pkg:npm/axios@1.12.0"}, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/lodash@4.17.21"}, contractResult: "PASS", wantExact: false,
		},
		{
			name: "omitted packages allow lodash failure", candidateSym: "lodash.get",
			clusterPackage: "lodash", clusterSym: "lodash.get",
			contract:     []string{"returns parsed JSON"},
			packages:     []string{"pkg:npm/axios@1.12.0", "pkg:npm/lodash@4.17.21"},
			omitPackages: true, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/lodash@4.17.21"}, contractResult: "PASS", wantExact: true,
		},
		{
			name: "explicit version excludes a different receipt version", candidateSym: "axios.get",
			clusterSym: "axios.get", contract: []string{"returns parsed JSON"}, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/axios@2.0.0"}, contractResult: "PASS", wantExact: false,
		},
		{
			name:         "multi-package failure on non-worst grading package",
			candidateSym: "lodash.get", clusterPackage: "lodash", clusterSym: "lodash.get",
			contract:        []string{"returns parsed JSON"},
			packages:        []string{"pkg:npm/axios@1.12.0", "pkg:npm/lodash@4.17.21"},
			requestPackages: []string{"pkg:npm/axios@2.0.0", "pkg:npm/lodash@4.17.21"},
			receiptSchema:   2, resolvedPackages: []string{"pkg:npm/lodash@4.17.21"},
			contractResult: "PASS", wantExact: true,
		},
		{
			name: "omitted package request", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"returns parsed JSON"}, omitPackages: true,
			receiptSchema: 2, resolvedPackages: []string{"pkg:npm/axios@1.12.0"},
			contractResult: "PASS", wantExact: true,
		},
		{
			name: "v1 pass has no resolved packages", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"returns parsed JSON"}, receiptSchema: 1,
			contractResult: "PASS", wantExact: false,
		},
		{
			name: "same package different symbol", candidateSym: "axios.post", clusterSym: "axios.get",
			contract: []string{"posts JSON"}, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/axios@1.12.0"}, contractResult: "PASS", wantExact: false,
		},
		{
			name: "blank cluster symbol", candidateSym: "axios.get", clusterSym: "",
			contract: []string{"returns parsed JSON"}, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/axios@1.12.0"}, contractResult: "PASS", wantExact: false,
		},
		{
			name: "no selected pass", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"returns parsed JSON"}, wantExact: false,
		},
		{
			name: "blank declared contract", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"  "}, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/axios@1.12.0"}, contractResult: "PASS", wantExact: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			ctx := context.Background()
			packages := tc.packages
			if len(packages) == 0 {
				packages = []string{"pkg:npm/axios@1.12.0"}
			}
			m := mkManifest("get JSON with axios",
				packages, nodeEnv("esm"), tc.candidateSym)
			m.Case.Contract = tc.contract
			if err := SeedSampleDoc(ctx, db, m, "sha256:candidate", "LOCAL_PASS"); err != nil {
				t.Fatal(err)
			}
			if tc.receiptSchema != 0 {
				saveReceiptWithPackages(t, db, "sha256:candidate", m,
					tc.receiptSchema, tc.resolvedPackages, tc.contractResult)
			}
			clusterPackage := tc.clusterPackage
			clusterPURL := "pkg:npm/axios@1.12.0"
			if clusterPackage == "" {
				clusterPackage = "axios"
			} else if clusterPackage == "lodash" {
				clusterPURL = "pkg:npm/lodash@4.17.21"
			}
			shardKey := "npm/" + clusterPackage + "/1"
			saveShardJSON(t, db, shardKey, shardFile{
				SchemaVersion: 1, Key: shardKey,
				GeneratedAt: time.Now().UTC().Format(time.RFC3339),
				Packages: []shardPackage{{
					PURL: clusterPURL,
					Symbols: []shardSymbolEntry{{
						Family:   tc.clusterSym,
						Stats:    shardSymbolStats{ObservationCount: 1, PassRate: 1},
						Failures: []shardFailure{{Fingerprint: fp, Count: 1}},
					}},
				}},
			})

			requestPackages := tc.requestPackages
			if len(requestPackages) == 0 && !tc.omitPackages {
				requestPackages = []string{"pkg:npm/axios@1.12.0"}
			}
			resp := (Engine{DB: db}).Search(ctx, domain.SearchRequest{
				SchemaVersion:    1,
				Query:            "get JSON with axios",
				Packages:         requestPackages,
				Environment:      nodeEnv("esm"),
				ErrorFingerprint: fp,
			})
			if resp.Miss || len(resp.Results) == 0 {
				t.Fatalf("expected candidate hit, got %+v", resp)
			}
			if got := resp.Results[0].ExactFailureMatched; got != tc.wantExact {
				t.Errorf("ExactFailureMatched = %v, want %v", got, tc.wantExact)
			}
		})
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
	// The two samples share one coordinate, so the answer is folded to one
	// result — and which one survives IS the ranking claim: the fold keeps
	// the highest-scored, so only CROSS_PASS outscoring LOCAL_PASS puts the
	// verified sample here.
	if resp.Miss || len(resp.Results) != 1 {
		t.Fatalf("expected the folded best result, got miss=%v n=%d", resp.Miss, len(resp.Results))
	}
	if resp.Results[0].SampleID != "sha256:crossc" {
		t.Fatalf("results[0] = %s, want the CROSS_PASS sample to win the coordinate", resp.Results[0].SampleID)
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

// Two samples for the same (packages, symbols) coordinate are the same
// answer twice. The HTTP search already folds them; this is the LOCAL engine
// — the path serving search_known_solution, the primary agent surface —
// where duplicates still consumed the 3-result budget and displaced the
// distinct second-best answer.
func TestDuplicateCoordinatesDoNotConsumeTheResultBudget(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	env := nodeEnv("cjs")
	for i, id := range []string{"sha256:dup1", "sha256:dup2", "sha256:dup3"} {
		m := mkManifest("post json with axios variant "+string(rune('a'+i)),
			[]string{"pkg:npm/axios@1.12.0"}, env, "axios.post")
		if err := SeedSampleDoc(ctx, db, m, id, "LOCAL_PASS"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	distinct := mkManifest("post json with axios interceptors",
		[]string{"pkg:npm/axios@1.12.0"}, env, "axios.interceptors")
	if err := SeedSampleDoc(ctx, db, distinct, "sha256:distinct", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed distinct: %v", err)
	}

	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1, Query: "post json axios",
		Packages:    []string{"pkg:npm/axios@1.12.0"},
		Environment: env,
	})
	if resp.Miss {
		t.Fatal("unexpected miss")
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want one per coordinate: %+v", len(resp.Results), resp.Results)
	}
	found := false
	for _, r := range resp.Results {
		if r.SampleID == "sha256:distinct" {
			found = true
		}
	}
	if !found {
		t.Fatal("the distinct second-best answer was displaced by duplicates")
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
	// Distinct symbols: identical coordinates would be folded to one result
	// before the limit is ever reached, and the limit is what this tests.
	symbols := []string{"axios.get", "axios.post", "axios.put", "axios.patch"}
	for i, id := range ids {
		m := mkManifest(goals[i], []string{"pkg:npm/axios@1.12.0"}, env, symbols[i])
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
