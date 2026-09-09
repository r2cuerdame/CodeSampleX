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

const safeUpgradeCase = "case:safe-upgrade"

// safeUpgradeSamples builds one comparable sample per measured version. Every
// dimension that makes two receipts comparable is held fixed, so the resolved
// version is the only thing that moves.
func safeUpgradeSamples(versions ...[2]string) []sampleData {
	env := envNode("esm")
	out := make([]sampleData, 0, len(versions))
	for _, v := range versions {
		version, result := v[0], v[1]
		out = append(out, sampleData{
			row: serverstore.SampleRow{CaseID: safeUpgradeCase},
			manifest: domain.SampleManifest{
				Case:    domain.Case{SchemaVersion: 1, CaseID: safeUpgradeCase},
				Symbols: []string{"axios.post"},
			},
			receipts: []ReceiptInfo{{
				CaseID: safeUpgradeCase, Env: env,
				Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": result},
				ContractResult:   result,
				ResolvedPackages: []domain.PURL{{Ecosystem: "npm", Name: "axios", Version: version}},
				VerifierAdapter:  "node-typescript@1", SandboxCapability: domain.CapContainerRun,
			}},
		})
	}
	return out
}

func safeUpgradeFor(samples []sampleData, version string) []SafeUpgradeCandidate {
	return safeUpgradesFromReceipts(samples)[receiptTarget{
		purl: "pkg:npm/axios@" + version, symbol: "axios.post",
	}]
}

// The suggestion is the highest version reachable along an unbroken run of
// measured passes. Every version it names was measured; the path is the
// evidence, never a range.
func TestSafeUpgradeReachesTheHighestUnbrokenMeasuredPass(t *testing.T) {
	samples := safeUpgradeSamples(
		[2]string{"1.0.0", "PASS"}, [2]string{"1.1.0", "PASS"}, [2]string{"1.2.0", "PASS"})

	got := safeUpgradeFor(samples, "1.0.0")
	if len(got) != 1 {
		t.Fatalf("safe upgrade candidates = %+v", got)
	}
	if got[0].TargetPackage != "pkg:npm/axios@1.2.0" {
		t.Errorf("target = %q, want the highest measured pass", got[0].TargetPackage)
	}
	if strings.Join(got[0].MeasuredPath, ",") != "1.1.0,1.2.0" {
		t.Errorf("measured path = %v, want every version the claim rests on", got[0].MeasuredPath)
	}
	if got[0].CurrentResult != string(domain.ResultPass) || got[0].Stage != string(domain.StageContract) {
		t.Errorf("candidate = %+v", got[0])
	}
	if got[0].BlockedByVersion != "" {
		t.Errorf("blockedBy = %q, want empty — nothing above it was measured", got[0].BlockedByVersion)
	}
	// Nothing was measured above 1.2.0, so 1.2.0 has nowhere evidence-backed
	// to go. Silence is the honest answer, not "you are already on the best".
	if got := safeUpgradeFor(samples, "1.2.0"); len(got) != 0 {
		t.Fatalf("claimed an upgrade with no measurement above it: %+v", got)
	}
}

// The path stops at the measured boundary and names the version that stopped
// it. This is the transition Regression Watch reports, read from below.
func TestSafeUpgradeStopsAtTheMeasuredBoundary(t *testing.T) {
	samples := safeUpgradeSamples(
		[2]string{"1.0.0", "PASS"}, [2]string{"1.1.0", "PASS"}, [2]string{"2.0.0", "FAIL"})

	got := safeUpgradeFor(samples, "1.0.0")
	if len(got) != 1 || got[0].TargetPackage != "pkg:npm/axios@1.1.0" {
		t.Fatalf("candidate did not stop below the failing release: %+v", got)
	}
	if got[0].BlockedByVersion != "2.0.0" {
		t.Errorf("blockedBy = %q, want the measured failure that ends the path", got[0].BlockedByVersion)
	}
	// From 1.1.0 the only measured step up is the failure itself.
	if got := safeUpgradeFor(samples, "1.1.0"); len(got) != 0 {
		t.Fatalf("recommended stepping onto a measured failure: %+v", got)
	}
}

// A version measured both ways proves nothing, and a claim may not step over
// it to reach a passing version further up.
func TestSafeUpgradeNeverStepsOverAnUnprovenVersion(t *testing.T) {
	samples := safeUpgradeSamples(
		[2]string{"1.0.0", "PASS"}, [2]string{"1.1.0", "PASS"}, [2]string{"1.1.0", "FAIL"},
		[2]string{"1.2.0", "PASS"})

	if got := safeUpgradeFor(samples, "1.0.0"); len(got) != 0 {
		t.Fatalf("claimed a path across a version with no unambiguous verdict: %+v", got)
	}
	// The unproven version is not a measured failure either, so nothing may
	// report it as blocking.
	for _, candidates := range safeUpgradesFromReceipts(samples) {
		for _, got := range candidates {
			if got.BlockedByVersion == "1.1.0" {
				t.Fatalf("reported an unproven version as a measured block: %+v", got)
			}
		}
	}
}

// The most useful case: the coordinate you are on failed. The nearest
// measured pass above it is a recovery, and the candidate says so.
func TestSafeUpgradeOffersRecoveryFromAMeasuredFailure(t *testing.T) {
	samples := safeUpgradeSamples(
		[2]string{"1.0.0", "PASS"}, [2]string{"1.1.0", "FAIL"}, [2]string{"1.2.0", "PASS"})

	got := safeUpgradeFor(samples, "1.1.0")
	if len(got) != 1 || got[0].TargetPackage != "pkg:npm/axios@1.2.0" {
		t.Fatalf("no recovery offered from a measured failure: %+v", got)
	}
	if got[0].CurrentResult != string(domain.ResultFail) {
		t.Errorf("currentResult = %q, want FAIL — the reader must know this is an escape",
			got[0].CurrentResult)
	}
	// 1.0.0 passes, but reaching 1.2.0 from it crosses a measured failure.
	if got := safeUpgradeFor(samples, "1.0.0"); len(got) != 0 {
		t.Fatalf("routed a passing coordinate through a measured failure: %+v", got)
	}
}

// A suggestion is only true under the conditions that produced it. Two
// versions measured under different environments, adapters, cases, sandboxes
// or dependency sets are not a path.
func TestSafeUpgradeNeverCrossesTheConditionsThatProvedIt(t *testing.T) {
	base := func() []sampleData {
		return safeUpgradeSamples([2]string{"1.0.0", "PASS"}, [2]string{"1.1.0", "PASS"})
	}
	if got := safeUpgradeFor(base(), "1.0.0"); len(got) != 1 {
		t.Fatalf("comparable baseline produced no candidate: %+v", got)
	}

	changes := map[string]func(*sampleData){
		"environment": func(sd *sampleData) { sd.receipts[0].Env.OS = "linux" },
		"adapter":     func(sd *sampleData) { sd.receipts[0].VerifierAdapter = "node-esm@1" },
		"case":        func(sd *sampleData) { sd.receipts[0].CaseID = "case:different" },
		"sandbox":     func(sd *sampleData) { sd.receipts[0].SandboxCapability = domain.CapCompileOnly },
		"companion dependency": func(sd *sampleData) {
			sd.receipts[0].ResolvedPackages = append(sd.receipts[0].ResolvedPackages,
				domain.PURL{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"})
		},
		"compile never passed": func(sd *sampleData) { sd.receipts[0].Stages["compile"] = "FAIL" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			samples := base()
			change(&samples[1])
			if got := safeUpgradeFor(samples, "1.0.0"); len(got) != 0 {
				t.Fatalf("crossed a difference in %s to claim a safe upgrade: %+v", name, got)
			}
		})
	}
}

// The candidate carries the conditions it was measured under, so no reader
// can widen an adapter-and-environment-specific path into a package-wide one.
func TestSafeUpgradeKeepsItsComparisonDimensions(t *testing.T) {
	samples := safeUpgradeSamples([2]string{"1.0.0", "PASS"}, [2]string{"1.1.0", "PASS"})
	got := safeUpgradeFor(samples, "1.0.0")
	if len(got) != 1 {
		t.Fatalf("candidates = %+v", got)
	}
	c := got[0]
	if c.Package != "pkg:npm/axios@1.0.0" || c.CaseID != safeUpgradeCase ||
		c.Symbol != "axios.post" || c.VerifierAdapter != "node-typescript@1" ||
		c.SandboxCapability != domain.CapContainerRun || c.HarnessHash == "" ||
		c.EnvBucketHash == "" || c.ContextLabel == "" {
		t.Fatalf("candidate dropped a comparison dimension: %+v", c)
	}
	if c.CurrentObservations != 1 || c.TargetObservations != 1 {
		t.Errorf("observation counts = (%d, %d), want the measurements behind each endpoint",
			c.CurrentObservations, c.TargetObservations)
	}
}

// A newer release changes what the OLDER coordinate's page should say, which
// is the opposite direction from a regression boundary. Both are covered by
// invalidating every known major of a dirty package.
func TestNewReceiptInvalidatesTheOlderCoordinateSafeUpgrade(t *testing.T) {
	affected := map[shardKey]bool{{ecosystem: "npm", name: "axios", major: "2"}: true}
	targets := []serverstore.SnapshotTarget{
		{PURL: "pkg:npm/axios@1.11.0"},
		{PURL: "pkg:npm/axios@2.0.0"},
	}
	expandAffectedPackageMajors(affected, targets)
	if !affected[shardKey{ecosystem: "npm", name: "axios", major: "1"}] {
		t.Fatal("a new release left the older coordinate's safe-upgrade snapshot stale")
	}
}

func TestBuilderPublishesSafeUpgradeOnTheCurrentCoordinate(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	caseID := "case:builder-safe-upgrade"
	env := envNode("esm")
	for i, endpoint := range []struct{ version, result string }{
		{version: "1.11.0", result: "PASS"},
		{version: "1.12.0", result: "PASS"},
		{version: "2.0.0", result: "FAIL"},
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
		sampleID := "sha256:" + strings.Repeat(string(rune('a'+i)), 64)
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
			PeerID: "ed25519:aaaaaaaaaaaaaaaa",
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

	read := func(purl string) Snapshot {
		t.Helper()
		raw, ok, err := store.GetSnapshot(ctx, purl, "axios.post")
		if err != nil || !ok {
			t.Fatalf("snapshot %s: ok=%v err=%v", purl, ok, err)
		}
		var snapshot Snapshot
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}

	current := read("pkg:npm/axios@1.11.0")
	if len(current.SafeUpgradeCandidates) != 1 {
		t.Fatalf("safe upgrade candidates = %+v", current.SafeUpgradeCandidates)
	}
	got := current.SafeUpgradeCandidates[0]
	if got.TargetPackage != "pkg:npm/axios@1.12.0" || got.BlockedByVersion != "2.0.0" ||
		got.CaseID != caseID {
		t.Fatalf("published candidate = %+v", got)
	}

	// The failing release keeps its regression boundary and offers no path up.
	broken := read("pkg:npm/axios@2.0.0")
	if len(broken.RegressionCandidates) != 1 {
		t.Fatalf("regression candidates = %+v", broken.RegressionCandidates)
	}
	if len(broken.SafeUpgradeCandidates) != 0 {
		t.Fatalf("offered an upgrade with nothing measured above: %+v", broken.SafeUpgradeCandidates)
	}
}
