package compatibility

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

var testNow = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

func envNode(moduleSystem string) domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18.1", ModuleSystem: moduleSystem,
	}
}

func envBrowser(family, major string) domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		ExecutionContext: "browser", BrowserFamily: family, BrowserMajor: major,
	}.Normalize()
}

func evRow(purl, symbol string, env domain.EnvironmentFingerprint, stage string,
	result string, count int64, fp, code string, peers int) serverstore.EvidenceRow {
	env = env.Normalize()
	return serverstore.EvidenceRow{
		PURL: purl, Symbol: symbol,
		SymbolConfidence: "PROBABLE",
		EnvHash:          env.Hash(),
		EnvJSON:          string(domain.MustCanonicalJSON(env)),
		Stage:            stage, Result: result,
		ErrorFingerprint: fp, ErrorCode: code,
		ObservationCount: count, UniquePeerBuckets: peers,
		FirstSeen: testNow.Add(-24 * time.Hour), LastSeen: testNow,
	}
}

// --- snapshot ---------------------------------------------------------------

func TestSnapshotRowsContextFirst(t *testing.T) {
	purl := "pkg:npm/axios@1.12.0"
	evidence := []serverstore.EvidenceRow{
		evRow(purl, "axios.post", envNode("esm"), "PROJECT_COMPILE", "PASS", 10, "", "", 4),
		evRow(purl, "axios.post", envBrowser("chrome", "140"), "PROJECT_LOAD", "PASS", 6, "", "", 2),
	}
	snap := BuildSnapshot(purl, "axios.post", evidence, nil, nil, testNow)

	if len(snap.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(snap.Rows))
	}
	// Context label is the leading dimension and orders the rows.
	if snap.Rows[0].ContextLabel != "chrome 140" {
		t.Fatalf("row 0 contextLabel = %q, want \"chrome 140\"", snap.Rows[0].ContextLabel)
	}
	if snap.Rows[1].ContextLabel != "node 22.18" {
		t.Fatalf("row 1 contextLabel = %q, want \"node 22.18\"", snap.Rows[1].ContextLabel)
	}
	// Environment buckets are the privacy-lowered (major.minor) form.
	if got := snap.Rows[1].EnvBucket.RuntimeVersion; got != "22.18" {
		t.Fatalf("bucketed runtimeVersion = %q, want \"22.18\"", got)
	}
	if snap.SchemaVersion != 1 || snap.PURL != purl || snap.Symbol != "axios.post" {
		t.Fatalf("snapshot header wrong: %+v", snap)
	}
}

func TestSnapshotMarksElevatedFailureRow(t *testing.T) {
	purl := "pkg:npm/axios@1.13.0"
	env := envNode("cjs")
	evidence := []serverstore.EvidenceRow{
		evRow(purl, "", env, "PROJECT_COMPILE", "PASS", 3, "", "", 2),
		evRow(purl, "", env, "PROJECT_COMPILE", "FAIL", 2,
			"sha256:"+strings.Repeat("ab", 32), "ERR_REQUIRE_ESM", 2),
	}
	snap := BuildSnapshot(purl, "", evidence, nil, nil, testNow)
	if len(snap.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(snap.Rows))
	}
	row := snap.Rows[0]
	if !row.ElevatedFailure {
		t.Fatalf("row not marked ELEVATED_FAILURE: passRate=%v", row.PassRate)
	}
	if row.ByStage["PROJECT_COMPILE"].Pass != 3 || row.ByStage["PROJECT_COMPILE"].Fail != 2 {
		t.Fatalf("byStage = %+v", row.ByStage)
	}
	if len(snap.Failures) != 1 || snap.Failures[0].ErrorCode != "ERR_REQUIRE_ESM" {
		t.Fatalf("failures = %+v", snap.Failures)
	}
}

func TestSnapshotSeparatesObservationFromVerification(t *testing.T) {
	purl := "pkg:npm/axios@1.12.0"
	env := envNode("esm")
	evidence := []serverstore.EvidenceRow{
		evRow(purl, "", env, "PROJECT_COMPILE", "PASS", 100, "", "", 3),
	}
	receipts := []ReceiptInfo{{
		PeerID: "ed25519:aaaaaaaaaaaaaaaa", Env: env,
		ContractResult: "PASS", Stages: map[string]string{"contract": "PASS"},
		CreatedAt: testNow,
	}}
	snap := BuildSnapshot(purl, "", evidence, receipts, nil, testNow)
	if len(snap.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(snap.Rows))
	}
	row := snap.Rows[0]
	if row.ObservationClassCounts["USAGE_OBSERVATION"] != 100 {
		t.Fatalf("observation counts = %+v", row.ObservationClassCounts)
	}
	if row.VerificationCounts["SAMPLE_VERIFICATION"] != 1 {
		t.Fatalf("verification counts = %+v", row.VerificationCounts)
	}
	// The two classes stay in separate maps — never a summed field.
	if row.ByStage["CONTRACT"].Pass != 1 || row.ByStage["PROJECT_COMPILE"].Pass != 100 {
		t.Fatalf("byStage = %+v", row.ByStage)
	}
}

// --- regression --------------------------------------------------------------

func TestRegressionFixtureFlagsCandidate(t *testing.T) {
	env := envNode("esm")
	cur := []serverstore.EvidenceRow{
		evRow("pkg:npm/axios@1.13.0", "", env, "PROJECT_COMPILE", "PASS", 1, "", "", 1),
		evRow("pkg:npm/axios@1.13.0", "", env, "PROJECT_COMPILE", "FAIL", 4,
			"sha256:"+strings.Repeat("cd", 32), "TS2345", 1),
	}
	prev := []serverstore.EvidenceRow{
		evRow("pkg:npm/axios@1.12.0", "", env, "PROJECT_COMPILE", "PASS", 10, "", "", 3),
	}
	regs := DetectRegressions("pkg:npm/axios@1.13.0", "pkg:npm/axios@1.12.0", "", cur, prev)
	if len(regs) != 1 {
		t.Fatalf("regressions = %d, want 1", len(regs))
	}
	r := regs[0]
	if r.FailRate < 0.25 || r.PreviousPassRate < 0.9 {
		t.Fatalf("thresholds not honored: %+v", r)
	}
	if r.Package != "pkg:npm/axios@1.13.0" || r.PreviousPackage != "pkg:npm/axios@1.12.0" {
		t.Fatalf("purls wrong: %+v", r)
	}
}

func TestRegressionNeedsMinObservationsAndSameBucket(t *testing.T) {
	env := envNode("esm")
	// Too few observations in the current version.
	cur := []serverstore.EvidenceRow{
		evRow("pkg:npm/a@2.0.0", "", env, "PROJECT_COMPILE", "FAIL", 4,
			"sha256:"+strings.Repeat("ef", 32), "", 1),
	}
	prev := []serverstore.EvidenceRow{
		evRow("pkg:npm/a@1.0.0", "", env, "PROJECT_COMPILE", "PASS", 10, "", "", 1),
	}
	if regs := DetectRegressions("pkg:npm/a@2.0.0", "pkg:npm/a@1.0.0", "", cur, prev); len(regs) != 0 {
		t.Fatalf("regressions = %v, want none (<5 obs)", regs)
	}
	// Different env bucket in the previous version: no comparison possible.
	otherEnv := envNode("cjs")
	cur[0].ObservationCount = 5
	prev = []serverstore.EvidenceRow{
		evRow("pkg:npm/a@1.0.0", "", otherEnv, "PROJECT_COMPILE", "PASS", 10, "", "", 1),
	}
	if regs := DetectRegressions("pkg:npm/a@2.0.0", "pkg:npm/a@1.0.0", "", cur, prev); len(regs) != 0 {
		t.Fatalf("regressions = %v, want none (bucket mismatch)", regs)
	}
}

func TestPreviousVersion(t *testing.T) {
	versions := []string{"1.12.0", "1.13.0", "1.11.2", "2.0.0"}
	if prev, ok := PreviousVersion(versions, "1.13.0"); !ok || prev != "1.12.0" {
		t.Fatalf("PreviousVersion(1.13.0) = %q,%v", prev, ok)
	}
	if prev, ok := PreviousVersion(versions, "2.0.0"); !ok || prev != "1.13.0" {
		t.Fatalf("PreviousVersion(2.0.0) = %q,%v", prev, ok)
	}
	if _, ok := PreviousVersion(versions, "1.11.2"); ok {
		t.Fatal("lowest version must have no predecessor")
	}
}

// --- clusters ----------------------------------------------------------------

func TestESMCodeYieldsConfigurationMajorHypotheses(t *testing.T) {
	hyps := Hypotheses("ERR_REQUIRE_ESM", nil, nil)
	if hyps[0].Domain != domain.FailConfiguration || hyps[0].Confidence != 0.72 {
		t.Fatalf("hyps = %+v, want CONFIGURATION 0.72 first", hyps)
	}
	total := 0.0
	for _, h := range hyps {
		total += h.Confidence
	}
	if total < 0.99 || total > 1.01 {
		t.Fatalf("hypothesis distribution sums to %v", total)
	}
}

func TestEngineConcentratedFailureYieldsEngineMajorHypotheses(t *testing.T) {
	fail := []domain.EnvironmentFingerprint{
		envBrowser("safari", "19"),
		envBrowser("ios-wkwebview", "19"),
	}
	pass := []domain.EnvironmentFingerprint{envBrowser("chrome", "140")}
	hyps := Hypotheses("", fail, pass)
	if hyps[0].Domain != domain.FailEngine || hyps[0].Confidence != 0.6 {
		t.Fatalf("hyps = %+v, want ENGINE 0.6 first", hyps)
	}
	// Never a definitive verdict.
	for _, h := range hyps {
		if h.Confidence >= 1.0 && h.Domain != domain.FailUnknown {
			t.Fatalf("definitive verdict emitted: %+v", h)
		}
	}
}

func TestUnknownFailureStaysUnknown(t *testing.T) {
	// Failures spread across engines: no engine concentration.
	fail := []domain.EnvironmentFingerprint{envBrowser("safari", "19"), envBrowser("chrome", "140")}
	hyps := Hypotheses("", fail, nil)
	if len(hyps) != 1 || hyps[0].Domain != domain.FailUnknown || hyps[0].Confidence != 1.0 {
		t.Fatalf("hyps = %+v, want UNKNOWN 1.0", hyps)
	}
}

func TestBuildClustersGroupsAcrossVersions(t *testing.T) {
	fp := "sha256:" + strings.Repeat("12", 32)
	byVersion := map[string][]serverstore.EvidenceRow{
		"1.12.0": {
			evRow("pkg:npm/axios@1.12.0", "axios.post", envBrowser("safari", "19"),
				"PROJECT_TEST", "FAIL", 3, fp, "", 1),
			evRow("pkg:npm/axios@1.12.0", "axios.post", envBrowser("chrome", "140"),
				"PROJECT_TEST", "PASS", 9, "", "", 3),
		},
		"1.13.0": {
			evRow("pkg:npm/axios@1.13.0", "axios.post", envBrowser("safari", "19"),
				"PROJECT_TEST", "FAIL", 2, fp, "", 1),
		},
	}
	clusters := BuildClusters("npm", "axios", byVersion, nil, testNow)
	if len(clusters) != 1 {
		t.Fatalf("clusters = %d, want 1", len(clusters))
	}
	c := clusters[0]
	if c.ObservationCount != 5 {
		t.Fatalf("count = %d, want 5 across versions", c.ObservationCount)
	}
	if c.VersionsJSON != `["1.12.0","1.13.0"]` {
		t.Fatalf("versions = %s", c.VersionsJSON)
	}
	var hyps []domain.FailureHypothesis
	if err := json.Unmarshal([]byte(c.HypothesesJSON), &hyps); err != nil {
		t.Fatal(err)
	}
	if hyps[0].Domain != domain.FailEngine {
		t.Fatalf("hypotheses = %+v, want ENGINE-major (webkit concentration)", hyps)
	}
	if !strings.Contains(c.EnvSummaryJSON, `"engine":"webkit"`) {
		t.Fatalf("env summary = %s, want shared engine", c.EnvSummaryJSON)
	}
}

// --- shard -------------------------------------------------------------------

func TestShardEtagStability(t *testing.T) {
	pkgs := []ShardPackage{{
		PURL: "pkg:npm/axios@1.12.0",
		Symbols: []ShardSymbol{{
			Family: "axios.post",
			Stats: ShardSymbolStats{
				ObservationCount: 123, UniquePeerBuckets: 9, PassRate: 0.94,
				ByStage:    map[string]StageCount{"PROJECT_COMPILE": {Pass: 100, Fail: 4}},
				Confidence: "HIGH",
			},
		}},
	}}
	js1, etag1 := BuildShard("npm/axios/1", pkgs, testNow)
	js2, etag2 := BuildShard("npm/axios/1", pkgs, testNow)
	if etag1 != etag2 || js1 != js2 {
		t.Fatalf("shard build is not deterministic: %s vs %s", etag1, etag2)
	}
	if len(etag1) != 64 {
		t.Fatalf("etag = %q, want sha256 hex", etag1)
	}
	var shard Shard
	if err := json.Unmarshal([]byte(js1), &shard); err != nil {
		t.Fatalf("shard json invalid: %v", err)
	}
	if shard.SchemaVersion != 1 || shard.Key != "npm/axios/1" {
		t.Fatalf("shard header: %+v", shard)
	}

	// A different generatedAt or content must change the etag.
	_, etag3 := BuildShard("npm/axios/1", pkgs, testNow.Add(time.Hour))
	if etag3 == etag1 {
		t.Fatal("etag must change when content changes")
	}
}

// --- builder end-to-end over the fake store ----------------------------------

func seedBuilderFixture(t *testing.T, store *serverstore.Fake) (samplePURL, sampleID string) {
	t.Helper()
	ctx := context.Background()
	samplePURL = "pkg:npm/axios@1.12.0"

	batch := func(anon, project, pkg, symbol string, env domain.EnvironmentFingerprint,
		stage domain.Stage, result domain.Result, count int, fp, code string) domain.ObservationBatch {
		return domain.ObservationBatch{
			SchemaVersion: 1, Epoch: "2026-08-13", AnonID: anon, ProjectBucket: project,
			Package: pkg, Symbol: symbol, SymbolConfidence: domain.SymbolProbable,
			Environment: env, Stage: stage, Result: result, ObservationCount: count,
			ErrorFingerprint: fp, ErrorCode: code,
		}
	}
	fp := "sha256:" + strings.Repeat("ab", 32)
	batches := []domain.ObservationBatch{
		batch("peer1", "proj1", samplePURL, "axios.post", envNode("esm"),
			domain.StageProjectCompile, domain.ResultPass, 10, "", ""),
		batch("peer2", "proj2", samplePURL, "axios.post", envNode("esm"),
			domain.StageProjectCompile, domain.ResultPass, 5, "", ""),
		batch("peer3", "proj3", samplePURL, "axios.post", envNode("cjs"),
			domain.StageProjectCompile, domain.ResultFail, 5, fp, "ERR_REQUIRE_ESM"),
		batch("peer3", "proj3", samplePURL, "axios.post", envNode("cjs"),
			domain.StageProjectCompile, domain.ResultPass, 2, "", ""),
	}
	if acc, rej, err := store.IngestBatches(ctx, batches); err != nil || acc != len(batches) || len(rej) != 0 {
		t.Fatalf("ingest: acc=%d rej=%v err=%v", acc, rej, err)
	}

	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{SchemaVersion: 1, Kind: "HOW", Goal: "post JSON with axios",
			Packages: []string{samplePURL}, Contract: []string{"posts JSON"}},
		Packages: []string{samplePURL}, Symbols: []string{"axios.post"},
		Environment: envNode("esm"), License: "MIT-0",
		ContractCommand: []string{"node", "test/contract.mjs"},
		VerifierAdapter: "node-typescript@1",
	}
	sampleID = "sha256:" + strings.Repeat("77", 32)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status: "CROSS_PASS", License: "MIT-0", SizeBytes: 1024, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	for i, envOS := range []string{"windows", "linux"} {
		env := envNode("esm")
		env.OS = envOS
		receipt := domain.VerificationReceipt{
			SchemaVersion: 2, SampleID: sampleID, CaseID: "case:x",
			EnvironmentHash: env.Normalize().Hash(), Environment: env,
			Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
			ResolvedPackages: []string{samplePURL},
			VerifierAdapter:  "node-typescript@1", SandboxCapability: domain.CapContainerRun,
			LogsDigest: "sha256:x", CreatedAt: testNow.Format(time.RFC3339),
			PeerID: "ed25519:" + strings.Repeat("ab", 8),
		}
		if i == 1 {
			receipt.PeerID = "ed25519:" + strings.Repeat("cd", 8)
		}
		if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
			ReceiptID: receipt.ReceiptID(), SampleID: sampleID, PeerID: receipt.PeerID,
			EnvHash:        receipt.EnvironmentHash,
			ReceiptJSON:    string(domain.MustCanonicalJSON(receipt)),
			ContractResult: "PASS", CreatedAt: testNow.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return samplePURL, sampleID
}

func TestBuilderRunOnce(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	purl, sampleID := seedBuilderFixture(t, store)

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Snapshot exists, rows are context-first, contract counted separately.
	js, ok, err := store.GetSnapshot(ctx, purl, "axios.post")
	if err != nil || !ok {
		t.Fatalf("snapshot missing: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(js), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Rows) < 2 {
		t.Fatalf("snapshot rows = %d, want >=2 (esm+cjs buckets)", len(snap.Rows))
	}
	for i := 1; i < len(snap.Rows); i++ {
		if snap.Rows[i-1].ContextLabel > snap.Rows[i].ContextLabel {
			t.Fatalf("rows not context-first ordered: %q > %q",
				snap.Rows[i-1].ContextLabel, snap.Rows[i].ContextLabel)
		}
	}
	var sawElevated, sawContract bool
	for _, row := range snap.Rows {
		if row.ElevatedFailure {
			sawElevated = true
		}
		if row.ByStage["CONTRACT"].Pass > 0 {
			sawContract = true
			if row.VerificationCounts["SAMPLE_VERIFICATION"] == 0 {
				t.Fatal("contract receipts must appear in verificationCounts")
			}
		}
	}
	if !sawElevated {
		t.Fatal("cjs bucket (2 pass / 5 fail) must be ELEVATED_FAILURE")
	}
	if !sawContract {
		t.Fatal("receipt evidence missing from snapshot rows")
	}

	// Failure cluster with CONFIGURATION-major hypotheses (ESM code).
	clusters, err := store.ListFailureClusters(ctx, "axios")
	if err != nil || len(clusters) != 1 {
		t.Fatalf("clusters = %d err=%v, want 1", len(clusters), err)
	}
	var hyps []domain.FailureHypothesis
	if err := json.Unmarshal([]byte(clusters[0].HypothesesJSON), &hyps); err != nil {
		t.Fatal(err)
	}
	if hyps[0].Domain != domain.FailConfiguration {
		t.Fatalf("hypotheses = %+v, want CONFIGURATION-major", hyps)
	}

	// Shard generated with a stable etag.
	etag1, shardJSON, ok, err := store.GetShard(ctx, "npm/axios/1")
	if err != nil || !ok {
		t.Fatalf("shard missing: %v", err)
	}
	var shard Shard
	if err := json.Unmarshal([]byte(shardJSON), &shard); err != nil {
		t.Fatal(err)
	}
	if len(shard.Packages) != 1 || shard.Packages[0].PURL != purl {
		t.Fatalf("shard packages = %+v", shard.Packages)
	}
	if len(shard.Packages[0].Samples) != 1 || shard.Packages[0].Samples[0].Status != "CROSS_PASS" {
		t.Fatalf("shard samples = %+v", shard.Packages[0].Samples)
	}
	if got := shard.Packages[0].Samples[0].Symbols; len(got) != 1 || got[0] != "axios.post" {
		t.Fatalf("manifest symbols missing from shard sample: %v", got)
	}
	if shard.Packages[0].Samples[0].SymbolsTruncated {
		t.Fatal("one valid manifest symbol was marked truncated")
	}
	if shard.Packages[0].CanonicalCaseCountTotal != 1 ||
		shard.Packages[0].DistinctSubjectCountTotal != 1 {
		t.Fatalf("shard depth totals = (%d, %d), want (1, 1)",
			shard.Packages[0].CanonicalCaseCountTotal,
			shard.Packages[0].DistinctSubjectCountTotal)
	}
	if shard.Packages[0].Samples[0].ContractStages["contract"] != "PASS" {
		t.Fatalf("contractStages = %+v", shard.Packages[0].Samples[0].ContractStages)
	}

	// Generic matrix wishes were not preparable by any pinned worker. Non-Java
	// samples therefore create no matrix work; exact Java lines are covered by
	// the focused test below.
	jobs, err := store.JobsForSample(ctx, sampleID)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.Reason == "matrix" {
			t.Fatalf("generic npm sample created unpreparable matrix job: %+v", j)
		}
	}

	// Stats rollup written with the estimated flag.
	statsJSON, ok, err := store.GetLatestStats(ctx)
	if err != nil || !ok {
		t.Fatalf("stats missing: %v", err)
	}
	var stats StatsDoc
	if err := json.Unmarshal([]byte(statsJSON), &stats); err != nil {
		t.Fatal(err)
	}
	if !stats.EstimatedReasoningAvoided.Estimated {
		t.Fatal("estimatedReasoningAvoided must always carry estimated:true")
	}
	if stats.Evidence != 22 {
		t.Fatalf("evidence = %d, want 22", stats.Evidence)
	}
	if stats.VerifiedSamples != 1 {
		t.Fatalf("verifiedSamples = %d, want 1", stats.VerifiedSamples)
	}

	// Idempotence: a second run with the same clock keeps the shard etag
	// stable and does not exceed the matrix-job cap.
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	etag2, _, _, _ := store.GetShard(ctx, "npm/axios/1")
	if etag2 != etag1 {
		t.Fatalf("shard etag changed across identical runs: %s → %s", etag1, etag2)
	}
	jobs, _ = store.JobsForSample(ctx, sampleID)
	matrix := 0
	for _, j := range jobs {
		if j.Reason == "matrix" {
			matrix++
		}
	}
	if matrix != 0 {
		t.Fatalf("matrix jobs = %d after rerun, want none for npm", matrix)
	}
}

func TestBuilderRunLoopStopsOnCancel(t *testing.T) {
	store := serverstore.NewFake()
	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.RunLoop(ctx, 10*time.Millisecond)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunLoop did not stop on context cancel")
	}
	if _, ok, _ := store.GetLatestStats(context.Background()); !ok {
		t.Fatal("RunLoop never completed a pass")
	}
}

func TestCreateMatrixJobsUsesOnlyExactPreparableJavaLines(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	b := &Builder{Store: store}
	m := domain.SampleManifest{
		VerifierAdapter: "maven-java@1",
		Environment: domain.EnvironmentFingerprint{
			Ecosystem: "maven", Runtime: "java", RuntimeVersion: "21", LanguageVersion: "17",
		},
	}
	sample := sampleData{
		row:      serverstore.SampleRow{SampleID: "sha256:java", Status: "CROSS_PASS"},
		manifest: m,
		receipts: []ReceiptInfo{{
			VerifierAdapter: "maven-java@1", SandboxCapability: domain.CapContainerRun,
			Env: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "maven", OS: "linux", OSVersionBucket: "2023",
				Distro: "amzn", Libc: "glibc", Runtime: "java", RuntimeVersion: "21",
				Language: "java", LanguageVersion: "17", Compiler: "javac", CompilerVersion: "21",
				PackageManager: "maven", PackageManagerVersion: "3.9.11", ExecutionContext: "java",
				Virtualization: "container", ContainerRuntime: "docker",
			},
		}},
	}
	// A legacy partial wish neither counts toward the exact matrix nor gets
	// recreated. Migration 0010 retires it in PostgreSQL.
	if _, err := store.CreateJob(ctx, serverstore.JobRow{SampleID: sample.row.SampleID, Reason: "matrix", WantEnvJSON: `{"runtime":"java","runtimeMajor":"17"}`}); err != nil {
		t.Fatal(err)
	}
	if err := b.createMatrixJobs(ctx, []sampleData{sample}); err != nil {
		t.Fatal(err)
	}
	if err := b.createMatrixJobs(ctx, []sampleData{sample}); err != nil {
		t.Fatal(err)
	}

	jobs, err := store.JobsForSample(ctx, sample.row.SampleID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, job := range jobs {
		var want domain.WorkerRequirements
		if json.Unmarshal([]byte(job.WantEnvJSON), &want) != nil || !exactJavaMatrixRequirements(want, m) {
			continue
		}
		got[want.RuntimeVersion]++
		if want.SandboxCapability != domain.CapContainerRun || want.VerifierAdapter != "maven-java@1" ||
			want.Ecosystem != "maven" || want.Runtime != "java" || want.ExecutionContext != "java" {
			t.Fatalf("incomplete worker requirements: %+v", want)
		}
	}
	if len(got) != 2 || got["17"] != 1 || got["25"] != 1 {
		t.Fatalf("exact runtime jobs = %#v, want 17 and 25 once (8/11 below language target; 21 covered)", got)
	}
}

func TestLegacyJavaReceiptDoesNotCoverPinnedMatrixCell(t *testing.T) {
	m := domain.SampleManifest{VerifierAdapter: "maven-java@1", Environment: domain.EnvironmentFingerprint{LanguageVersion: "17"}}
	legacy := ReceiptInfo{
		VerifierAdapter: "maven-java@1", SandboxCapability: domain.CapContainerRun,
		Env: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "maven", OS: "linux", OSVersionBucket: "alpine",
			Distro: "alpine", Libc: "musl", Runtime: "java", RuntimeVersion: "21",
			Compiler: "javac", CompilerVersion: "21", PackageManager: "maven", PackageManagerVersion: "3.9",
			ExecutionContext: "java", Virtualization: "container", ContainerRuntime: "docker",
		},
	}
	if receiptCoversExactJavaMatrix(legacy, m) {
		t.Fatal("legacy Temurin/Alpine receipt suppressed the Corretto/AL2023 Java 21 cell")
	}
}

// TestShardBuiltForSampleWithoutObservations pins the discovery path for a
// freshly seeded package: the answer exists before anyone has used the
// library. Shard keys used to come from observation evidence alone, so
// such a package got no shard — and since clients only ever read shards,
// its sample was invisible to every one of them.
func TestShardBuiltForSampleWithoutObservations(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }

	// A sample for a package with zero observation evidence.
	purl := "pkg:cargo/semver@1.0.28"
	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW", Goal: "check a caret requirement",
			Packages: []string{purl}, Contract: []string{"matches inside the range"},
		},
		Packages:        []string{purl},
		ContractCommand: []string{"cargo", "run", "--offline"},
		VerifierAdapter: "cargo@1",
		Environment:     domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "cargo"},
	}
	sampleID := "sha256:" + strings.Repeat("cd", 32)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status: "PUBLISHED", License: "MIT-0", SizeBytes: 1024, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	_, js, ok, err := store.GetShard(ctx, "cargo/semver/1")
	if err != nil || !ok {
		t.Fatalf("no shard for a package that has a sample but no observations (err=%v)", err)
	}
	if !strings.Contains(js, sampleID) {
		t.Fatalf("shard does not carry the sample:\n%s", js)
	}
}

// TestIncrementalPassSkipsUnchangedWork pins the fix for the aggregation
// pass that rebuilt the entire network every five minutes. Measured on
// production before this: 1,603 shard writes and 1,958 snapshot writes per
// pass while zero evidence rows had changed. That cost scales with the size
// of the graph rather than with what happened, which is what would flatten
// a small instance as the network grows.
func TestIncrementalPassSkipsUnchangedWork(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	seedBuilderFixture(t, store)

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}

	// Pass 1 is always full: nothing is materialized yet.
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	afterFirst := store.ShardWrites()
	if afterFirst == 0 {
		t.Fatal("first pass wrote no shards")
	}

	// Nothing has changed since. Subsequent passes must do no shard work.
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		return serverstore.Changes{}, nil
	}
	for i := 0; i < 5; i++ {
		if err := b.RunOnce(ctx); err != nil {
			t.Fatalf("idle pass %d: %v", i, err)
		}
	}
	if got := store.ShardWrites(); got != afterFirst {
		t.Errorf("idle passes wrote %d shards, want 0 beyond the first pass's %d",
			got-afterFirst, afterFirst)
	}

	// Stats must still refresh on an idle pass — they carry the counters
	// and the generatedAt timestamp the website renders.
	if js, ok, err := store.GetLatestStats(ctx); err != nil || !ok || js == "" {
		t.Errorf("idle pass did not refresh stats: ok=%v err=%v", ok, err)
	}

	// A change in one package brings its shard back, and only its shard.
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		return serverstore.Changes{
			Targets: []serverstore.SnapshotTarget{
				{PURL: "pkg:npm/axios@1.12.0", Symbol: "axios.post"},
			},
		}, nil
	}
	before := store.ShardWrites()
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("changed pass: %v", err)
	}
	wrote := store.ShardWrites() - before
	if wrote == 0 {
		t.Error("a changed package did not rebuild its shard")
	}
	if wrote >= afterFirst && afterFirst > 1 {
		t.Errorf("changed pass wrote %d shards, want fewer than a full pass's %d", wrote, afterFirst)
	}
}

// TestFullPassSelfHeals: incremental rebuilds trust a change query, so the
// builder must periodically rebuild everything anyway. Without this a
// missed change would leave a shard stale indefinitely.
func TestFullPassSelfHeals(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	seedBuilderFixture(t, store)
	// Always claims nothing changed — the pathological case this guards.
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		return serverstore.Changes{}, nil
	}

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil { // pass 1: full
		t.Fatal(err)
	}
	baseline := store.ShardWrites()

	// Passes 2..fullPassEvery are incremental and idle.
	for i := 1; i < fullPassEvery; i++ {
		if err := b.RunOnce(ctx); err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
	}
	if store.ShardWrites() != baseline {
		t.Fatalf("idle passes did work: %d beyond baseline", store.ShardWrites()-baseline)
	}

	// The next one is the scheduled full pass.
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if store.ShardWrites() <= baseline {
		t.Errorf("pass %d should have been a full rebuild, wrote nothing", fullPassEvery+1)
	}
}

// Regression detection (§10.3) compares version V against V-1 in the same
// environment bucket. On an incremental aggregation pass, keepTargets narrowed
// snapshot targets to the dirty shard keys, dropping versions belonging to
// other majors. PreviousVersion was given only the versions loaded for this
// pass, so a new major (e.g. 8.0.0) could not see its predecessor (7.0.3).
// DetectRegressions was skipped across major boundaries, leaving the snapshot
// with empty regression candidates and missing the regression badge until
// the next scheduled full pass.
func TestRegressionDetectedAcrossMajorVersionBoundaryOnIncrementalPass(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }

	batch := func(anon, project, pkg, symbol string, env domain.EnvironmentFingerprint,
		stage domain.Stage, result domain.Result, count int, fp, code string) domain.ObservationBatch {
		return domain.ObservationBatch{
			SchemaVersion: 1, Epoch: "2026-08-13", AnonID: anon, ProjectBucket: project,
			Package: pkg, Symbol: symbol, SymbolConfidence: domain.SymbolProbable,
			Environment: env, Stage: stage, Result: result, ObservationCount: count,
			ErrorFingerprint: fp, ErrorCode: code,
		}
	}

	env := envNode("esm")
	// 1. Package uuid has version pkg:npm/uuid@7.0.3 with 10 PASS observations on symbol v4 at stage PROJECT_COMPILE.
	passBatches := []domain.ObservationBatch{
		batch("peer1", "proj1", "pkg:npm/uuid@7.0.3", "v4", env,
			domain.StageProjectCompile, domain.ResultPass, 10, "", ""),
	}
	if _, _, err := store.IngestBatches(ctx, passBatches); err != nil {
		t.Fatalf("ingest pass batches: %v", err)
	}

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}

	// Initial full pass aggregates 7.0.3.
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("initial pass: %v", err)
	}

	// 2. A new version pkg:npm/uuid@8.0.0 gets 10 FAIL observations on symbol v4 in the same environment.
	failBatches := []domain.ObservationBatch{
		batch("peer2", "proj2", "pkg:npm/uuid@8.0.0", "v4", env,
			domain.StageProjectCompile, domain.ResultFail, 10,
			"sha256:"+strings.Repeat("ef", 32), "ERR_BREAKING"),
	}
	if _, _, err := store.IngestBatches(ctx, failBatches); err != nil {
		t.Fatalf("ingest fail batches: %v", err)
	}

	// 3. An incremental pass runs where uuid@8.0.0 changed.
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		return serverstore.Changes{
			Targets: []serverstore.SnapshotTarget{
				{PURL: "pkg:npm/uuid@8.0.0", Symbol: "v4"},
			},
		}, nil
	}

	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("incremental pass: %v", err)
	}

	js, ok, err := store.GetSnapshot(ctx, "pkg:npm/uuid@8.0.0", "v4")
	if err != nil || !ok {
		t.Fatalf("snapshot for uuid@8.0.0 v4 missing: ok=%v err=%v", ok, err)
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(js), &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if len(snap.RegressionCandidates) != 1 {
		t.Fatalf("expected 1 regression candidate for uuid@8.0.0 against 7.0.3, got %d", len(snap.RegressionCandidates))
	}
	reg := snap.RegressionCandidates[0]
	if reg.Package != "pkg:npm/uuid@8.0.0" || reg.PreviousPackage != "pkg:npm/uuid@7.0.3" {
		t.Errorf("regression candidate package pair = (%q, %q), want (pkg:npm/uuid@8.0.0, pkg:npm/uuid@7.0.3)",
			reg.Package, reg.PreviousPackage)
	}
	if reg.Symbol != "v4" {
		t.Errorf("regression candidate symbol = %q, want \"v4\"", reg.Symbol)
	}
	if reg.Stage != "PROJECT_COMPILE" {
		t.Errorf("regression candidate stage = %q, want \"PROJECT_COMPILE\"", reg.Stage)
	}
}
