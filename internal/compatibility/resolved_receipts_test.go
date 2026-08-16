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

func TestParseReceiptRowOnlyTreatsV2ResolveOutputAsVersionEvidence(t *testing.T) {
	base := domain.VerificationReceipt{
		SchemaVersion: 2, SampleID: "sha256:" + strings.Repeat("a", 64),
		CaseID: "case:stable", Environment: envNode("esm"),
		Stages:            map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
		ResolvedPackages:  []string{"pkg:npm/axios@1.12.4"},
		VerifierAdapter:   "node-typescript@1",
		SandboxCapability: domain.CapContainerRun,
		PeerID:            "ed25519:aaaaaaaaaaaaaaaa",
	}
	row := serverstore.ReceiptRow{
		PeerID: base.PeerID, ContractResult: "PASS", CreatedAt: testNow,
		ReceiptJSON: string(domain.MustCanonicalJSON(base)),
	}
	got, ok := ParseReceiptRow(row)
	if !ok || got.CaseID != base.CaseID || len(got.ResolvedPackages) != 1 ||
		got.ResolvedPackages[0].String() != base.ResolvedPackages[0] {
		t.Fatalf("v2 receipt info = %+v, ok=%v", got, ok)
	}

	base.SchemaVersion = 1
	row.ReceiptJSON = string(domain.MustCanonicalJSON(base))
	got, ok = ParseReceiptRow(row)
	if !ok {
		t.Fatal("a readable v1 receipt should remain usable as non-version lifecycle evidence")
	}
	if len(got.ResolvedPackages) != 0 {
		t.Fatalf("v1 receipt established versions: %+v", got.ResolvedPackages)
	}

	base.SchemaVersion = 2
	base.Stages["resolve"] = "FAIL"
	row.ReceiptJSON = string(domain.MustCanonicalJSON(base))
	got, ok = ParseReceiptRow(row)
	if !ok || len(got.ResolvedPackages) != 0 {
		t.Fatalf("failed resolve established versions: %+v", got.ResolvedPackages)
	}
}

func TestBuilderAttributesReceiptToResolvedVersionNotManifestVersion(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	declared := "pkg:npm/axios@1.0.0"
	resolved := "pkg:npm/axios@2.12.4"
	caseID := "case:stable-axios-post"
	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, CaseID: caseID, Kind: "HOW", Goal: "post JSON",
			Packages: []string{declared}, Contract: []string{"posts JSON"},
		},
		Packages: []string{declared}, Symbols: []string{"axios.post"},
		Environment: envNode("esm"), License: "MIT-0",
		ContractCommand: []string{"node", "test.mjs"}, VerifierAdapter: "node-typescript@1",
	}
	sampleID := "sha256:" + strings.Repeat("b", 64)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID: sampleID, CaseID: caseID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status: "CROSS_PASS", License: "MIT-0", CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	receipt := domain.VerificationReceipt{
		SchemaVersion: 2, SampleID: sampleID, CaseID: caseID,
		EnvironmentHash: manifest.Environment.Normalize().Hash(), Environment: manifest.Environment,
		Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
		ResolvedPackages: []string{resolved}, VerifierAdapter: manifest.VerifierAdapter,
		SandboxCapability: domain.CapContainerRun, CreatedAt: testNow.Format(time.RFC3339),
		PeerID: "ed25519:bbbbbbbbbbbbbbbb",
	}
	if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
		ReceiptID: receipt.ReceiptID(), SampleID: sampleID, PeerID: receipt.PeerID,
		ReceiptJSON: string(domain.MustCanonicalJSON(receipt)), ContractResult: "PASS", CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	js, ok, err := store.GetSnapshot(ctx, resolved, "axios.post")
	if err != nil || !ok {
		t.Fatalf("resolved target snapshot: ok=%v err=%v", ok, err)
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(js), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Rows) != 1 || snap.Rows[0].ByStage[string(domain.StageContract)].Pass != 1 {
		t.Fatalf("resolved snapshot did not receive the receipt: %+v", snap.Rows)
	}
	if _, ok, _ := store.GetSnapshot(ctx, declared, "axios.post"); ok {
		t.Fatal("manifest-only version received a receipt-derived snapshot")
	}
	if pkg, ok, err := store.GetPackage(ctx, resolved); err != nil || !ok ||
		pkg.Version != "2.12.4" || pkg.Publicness != "UNKNOWN" {
		t.Fatalf("receipt-only version was not registered: %+v ok=%v err=%v", pkg, ok, err)
	}

	for key, wantPURL := range map[string]string{
		"npm/axios/1": declared,
		"npm/axios/2": resolved,
	} {
		_, raw, ok, err := store.GetShard(ctx, key)
		if err != nil || !ok {
			t.Fatalf("shard %s: ok=%v err=%v", key, ok, err)
		}
		var shard Shard
		if err := json.Unmarshal([]byte(raw), &shard); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, pkg := range shard.Packages {
			if pkg.PURL != wantPURL || len(pkg.Samples) == 0 {
				continue
			}
			found = true
			sample := pkg.Samples[0]
			if len(sample.Packages) != 1 || sample.Packages[0] != declared {
				t.Errorf("declared packages were not retained: %v", sample.Packages)
			}
			if len(sample.Verifications) != 1 ||
				len(sample.Verifications[0].ResolvedPackages) != 1 ||
				sample.Verifications[0].ResolvedPackages[0] != resolved {
				t.Errorf("verifications = %+v", sample.Verifications)
			}
		}
		if !found {
			t.Fatalf("resolved purl/sample absent from shard: %s", raw)
		}
	}
	// An orphan outside this incremental pass's affected package is not ours
	// to delete. It may be waiting on a different dirty event; only a full
	// pass may globally prune it.
	unrelated := serverstore.SnapshotTarget{PURL: "pkg:npm/zod@3.0.0", Symbol: "z.parse"}
	if err := store.PutSnapshot(ctx, unrelated.PURL, unrelated.Symbol, `{"schemaVersion":1}`); err != nil {
		t.Fatal(err)
	}

	if err := store.SetSampleQuarantine(ctx, sampleID, true, "withdrawn"); err != nil {
		t.Fatal(err)
	}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, symbol := range []string{"", "axios.post"} {
		if _, ok, err := store.GetSnapshot(ctx, resolved, symbol); err != nil || ok {
			t.Fatalf("quarantined receipt snapshot %q survived: ok=%v err=%v", symbol, ok, err)
		}
	}
	if _, ok, err := store.GetSnapshot(ctx, unrelated.PURL, unrelated.Symbol); err != nil || !ok {
		t.Fatalf("incremental retirement deleted unrelated snapshot: ok=%v err=%v", ok, err)
	}
}

func TestReceiptRegressionRequiresOneComparableStableCaseBoundary(t *testing.T) {
	caseID := "case:stable-boundary"
	env := envNode("esm")
	makeSample := func(version, result string) sampleData {
		manifest := domain.SampleManifest{
			Case:    domain.Case{SchemaVersion: 1, CaseID: caseID},
			Symbols: []string{"axios.post"},
		}
		return sampleData{
			row: serverstore.SampleRow{CaseID: caseID}, manifest: manifest,
			receipts: []ReceiptInfo{{
				CaseID: caseID, Env: env,
				Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": result},
				ContractResult:   result,
				ResolvedPackages: []domain.PURL{{Ecosystem: "npm", Name: "axios", Version: version}},
				VerifierAdapter:  "node-typescript@1", SandboxCapability: domain.CapContainerRun,
			}},
			purls: []domain.PURL{{Ecosystem: "npm", Name: "axios", Version: version}},
		}
	}
	samples := []sampleData{makeSample("1.11.0", "PASS"), makeSample("1.12.0", "FAIL")}
	got := regressionsFromReceipts(samples)[receiptTarget{
		purl: "pkg:npm/axios@1.12.0", symbol: "axios.post",
	}]
	if len(got) != 1 {
		t.Fatalf("receipt regressions = %+v", got)
	}
	if got[0].PreviousPackage != "pkg:npm/axios@1.11.0" ||
		got[0].Stage != string(domain.StageContract) || got[0].CaseID != caseID {
		t.Fatalf("candidate = %+v", got[0])
	}

	bad := makeSample("1.12.0", "FAIL")
	bad.receipts[0].CaseID = "case:different"
	if got := regressionsFromReceipts([]sampleData{samples[0], bad}); len(got) != 0 {
		t.Fatalf("different receipt case id produced a boundary: %+v", got)
	}
	bad = makeSample("1.12.0", "FAIL")
	bad.receipts[0].Stages["compile"] = "FAIL"
	if got := regressionsFromReceipts([]sampleData{samples[0], bad}); len(got) != 0 {
		t.Fatalf("compile failure produced a contract boundary: %+v", got)
	}
	bad = makeSample("1.12.0", "FAIL")
	bad.receipts[0].Env.OS = "windows-other"
	if got := regressionsFromReceipts([]sampleData{samples[0], bad}); len(got) != 0 {
		t.Fatalf("different environment produced a boundary: %+v", got)
	}
	prevWithCompanion := makeSample("1.11.0", "PASS")
	prevWithCompanion.receipts[0].ResolvedPackages = append(
		prevWithCompanion.receipts[0].ResolvedPackages,
		domain.PURL{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"},
	)
	curWithChangedCompanion := makeSample("1.12.0", "FAIL")
	curWithChangedCompanion.receipts[0].ResolvedPackages = append(
		curWithChangedCompanion.receipts[0].ResolvedPackages,
		domain.PURL{Ecosystem: "npm", Name: "lodash", Version: "5.0.0"},
	)
	if got := regressionsFromReceipts([]sampleData{prevWithCompanion, curWithChangedCompanion}); len(got) != 0 {
		t.Fatalf("two dependencies changing produced a library boundary: %+v", got)
	}
}

func TestReceiptChangeInvalidatesEveryKnownMajorForRegressionBoundary(t *testing.T) {
	affected := map[shardKey]bool{{ecosystem: "npm", name: "axios", major: "1"}: true}
	targets := []serverstore.SnapshotTarget{
		{PURL: "pkg:npm/axios@1.12.4"},
		{PURL: "pkg:npm/axios@2.0.0"},
		{PURL: "pkg:npm/zod@3.0.0"},
	}
	expandAffectedPackageMajors(affected, targets)
	if !affected[shardKey{ecosystem: "npm", name: "axios", major: "2"}] {
		t.Fatal("old endpoint change did not invalidate the newer major's regression snapshot")
	}
	if affected[shardKey{ecosystem: "npm", name: "zod", major: "3"}] {
		t.Fatal("invalidated an unrelated package")
	}
}

func TestReceiptRegressionUsesAdjacentUnambiguousMeasuredVersions(t *testing.T) {
	caseID := "case:adjacent"
	env := envNode("esm")
	makeInfo := func(version, result string) ReceiptInfo {
		return ReceiptInfo{
			CaseID: caseID, Env: env,
			Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": result},
			ResolvedPackages: []domain.PURL{{Ecosystem: "npm", Name: "axios", Version: version}},
			VerifierAdapter:  "node-typescript@1", SandboxCapability: domain.CapContainerRun,
		}
	}
	makeData := func(version string, receipts ...ReceiptInfo) sampleData {
		return sampleData{
			row:      serverstore.SampleRow{CaseID: caseID},
			manifest: domain.SampleManifest{Case: domain.Case{SchemaVersion: 1, CaseID: caseID}, Symbols: []string{"get"}},
			receipts: receipts,
		}
	}
	samples := []sampleData{
		makeData("1.0.0", makeInfo("1.0.0", "PASS")),
		makeData("1.1.0", makeInfo("1.1.0", "PASS")),
		makeData("2.0.0", makeInfo("2.0.0", "FAIL")),
	}
	got := regressionsFromReceipts(samples)[receiptTarget{purl: "pkg:npm/axios@2.0.0", symbol: "get"}]
	if len(got) != 1 || got[0].PreviousPackage != "pkg:npm/axios@1.1.0" {
		t.Fatalf("boundary did not use the adjacent measured release: %+v", got)
	}

	// Conflicting receipts at one endpoint are not a PASS/FAIL boundary.
	samples[2].receipts = append(samples[2].receipts, makeInfo("2.0.0", "PASS"))
	if got := regressionsFromReceipts(samples); len(got) != 0 {
		t.Fatalf("ambiguous endpoint produced a boundary: %+v", got)
	}
}

func TestBuilderPublishesReceiptEstablishedRegressionOnExactTarget(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	caseID := "case:builder-receipt-boundary"
	env := envNode("esm")
	for i, endpoint := range []struct {
		version string
		result  string
	}{
		{version: "1.11.0", result: "PASS"},
		{version: "1.12.0", result: "FAIL"},
	} {
		purl := "pkg:npm/axios@" + endpoint.version
		manifest := domain.SampleManifest{
			SchemaVersion: 1,
			Case: domain.Case{SchemaVersion: 1, CaseID: caseID, Kind: "FIX", Goal: "post JSON",
				Packages: []string{purl}, Contract: []string{"posts JSON"}},
			Packages: []string{purl}, Symbols: []string{"axios.post"}, Environment: env,
			License: "MIT-0", ContractCommand: []string{"node", "test.mjs"},
			VerifierAdapter: "node-typescript@1",
		}
		sampleID := "sha256:" + strings.Repeat(string(rune('d'+i)), 64)
		if err := store.SaveSample(ctx, serverstore.SampleRow{
			SampleID: sampleID, CaseID: caseID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
			Status: "PUBLISHED", License: "MIT-0", CreatedAt: testNow.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		receipt := domain.VerificationReceipt{
			SchemaVersion: 2, SampleID: sampleID, CaseID: caseID,
			EnvironmentHash: env.Normalize().Hash(), Environment: env,
			Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": endpoint.result},
			ResolvedPackages: []string{purl}, VerifierAdapter: manifest.VerifierAdapter,
			SandboxCapability: domain.CapContainerRun, CreatedAt: testNow.Format(time.RFC3339),
			PeerID: "ed25519:dddddddddddddddd",
		}
		if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
			ReceiptID: receipt.ReceiptID(), SampleID: sampleID, PeerID: receipt.PeerID,
			ReceiptJSON:    string(domain.MustCanonicalJSON(receipt)),
			ContractResult: endpoint.result, CreatedAt: testNow.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	builder := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := builder.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	raw, ok, err := store.GetSnapshot(ctx, "pkg:npm/axios@1.12.0", "axios.post")
	if err != nil || !ok {
		t.Fatalf("current snapshot: ok=%v err=%v", ok, err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RegressionCandidates) != 1 {
		t.Fatalf("regression candidates = %+v", snapshot.RegressionCandidates)
	}
	got := snapshot.RegressionCandidates[0]
	if got.PreviousPackage != "pkg:npm/axios@1.11.0" || got.CaseID != caseID ||
		got.Stage != string(domain.StageContract) {
		t.Fatalf("receipt regression = %+v", got)
	}
}
