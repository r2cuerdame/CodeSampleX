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

const regressionWatchCase = "case:regression-watch"

// watchMeasurement is one dated receipt against one resolved version.
type watchMeasurement struct {
	version string
	result  string
	at      time.Time
}

// watchSamples builds one comparable sample per measurement, every comparison
// dimension held fixed, so version and time are the only things that move.
func watchSamples(measurements ...watchMeasurement) []sampleData {
	env := envNode("esm")
	out := make([]sampleData, 0, len(measurements))
	for _, m := range measurements {
		out = append(out, sampleData{
			row: serverstore.SampleRow{CaseID: regressionWatchCase},
			manifest: domain.SampleManifest{
				Case:    domain.Case{SchemaVersion: 1, CaseID: regressionWatchCase},
				Symbols: []string{"axios.post"},
			},
			receipts: []ReceiptInfo{{
				CaseID: regressionWatchCase, Env: env,
				Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": m.result},
				ContractResult:   m.result,
				ResolvedPackages: []domain.PURL{{Ecosystem: "npm", Name: "axios", Version: m.version}},
				VerifierAdapter:  "node-typescript@1", SandboxCapability: domain.CapContainerRun,
				CreatedAt: m.at,
			}},
		})
	}
	return out
}

func watchFor(samples []sampleData, version string) []RegressionWatchCandidate {
	return regressionWatchFromReceipts(samples)[receiptTarget{
		purl: "pkg:npm/axios@" + version, symbol: "axios.post",
	}]
}

var (
	day1 = testNow.Add(-72 * time.Hour)
	day2 = testNow.Add(-48 * time.Hour)
	day3 = testNow.Add(-24 * time.Hour)
)

// A coordinate that passed every time and then failed is a known-good
// coordinate that changed. The old verdict is kept on the candidate — the
// new measurement does not erase it — and the candidate asks for
// revalidation rather than declaring either side right.
func TestRegressionWatchKeepsTheKnownGoodVerdictWhenNewerEvidenceFails(t *testing.T) {
	samples := watchSamples(
		watchMeasurement{"1.1.0", "PASS", day1},
		watchMeasurement{"1.1.0", "PASS", day2},
		watchMeasurement{"1.1.0", "FAIL", day3},
	)
	got := watchFor(samples, "1.1.0")
	if len(got) != 1 {
		t.Fatalf("watch candidates = %+v", got)
	}
	c := got[0]
	if c.Kind != WatchKnownGoodNowFailing {
		t.Errorf("kind = %q", c.Kind)
	}
	if c.KnownResult != string(domain.ResultPass) || c.KnownObservations != 2 ||
		c.KnownThrough != day2.UTC().Format(time.RFC3339) {
		t.Errorf("known side = %s ×%d through %s; the earlier verdict must survive intact",
			c.KnownResult, c.KnownObservations, c.KnownThrough)
	}
	if c.LatestResult != string(domain.ResultFail) || c.LatestObservations != 1 ||
		c.LatestSince != day3.UTC().Format(time.RFC3339) {
		t.Errorf("latest side = %s ×%d since %s", c.LatestResult, c.LatestObservations, c.LatestSince)
	}
	if c.Package != "pkg:npm/axios@1.1.0" || c.CaseID != regressionWatchCase || c.Symbol != "axios.post" ||
		c.Stage != string(domain.StageContract) || c.VerifierAdapter != "node-typescript@1" ||
		c.SandboxCapability != domain.CapContainerRun || c.HarnessHash == "" ||
		c.EnvBucketHash == "" || c.ContextLabel == "" {
		t.Errorf("candidate dropped a comparison dimension: %+v", c)
	}
	// The contested coordinate anchors no boundary and no upgrade path: the
	// watch is where it is visible, and nowhere else.
	if regs := regressionsFromReceipts(samples); len(regs) != 0 {
		t.Errorf("a contested coordinate anchored a boundary: %+v", regs)
	}
}

// The other direction: a measured failure that newer evidence contradicts.
// The boundary it used to sit on is gone from the boundary list, so the
// candidate names the pass below it — that is the boundary's history.
func TestRegressionWatchNamesTheBoundaryANewPassCrosses(t *testing.T) {
	samples := watchSamples(
		watchMeasurement{"1.1.0", "PASS", day1},
		watchMeasurement{"2.0.0", "FAIL", day1},
		watchMeasurement{"2.0.0", "PASS", day3},
	)
	got := watchFor(samples, "2.0.0")
	if len(got) != 1 {
		t.Fatalf("watch candidates = %+v", got)
	}
	c := got[0]
	if c.Kind != WatchKnownFailureNowPassing || c.KnownResult != string(domain.ResultFail) ||
		c.LatestResult != string(domain.ResultPass) {
		t.Errorf("candidate = %+v", c)
	}
	if c.NearestLowerPassPackage != "pkg:npm/axios@1.1.0" {
		t.Errorf("nearest lower pass = %q, want the pass side of the boundary this crosses",
			c.NearestLowerPassPackage)
	}
	// 1.1.0 → 2.0.0 was a boundary until day3. It is contested now and must
	// not be reported as a boundary, and the safe path from 1.1.0 must not
	// step onto the contested release either.
	if regs := regressionsFromReceipts(samples); len(regs) != 0 {
		t.Errorf("a contested release still anchored a boundary: %+v", regs)
	}
	if ups := safeUpgradesFromReceipts(samples); len(ups) != 0 {
		t.Errorf("a contested release was offered as an upgrade: %+v", ups)
	}
}

// Interleaved verdicts are not a change over time; they are a coordinate
// nothing has decided. Silence, not a watch entry, because a watch entry
// says which verdict is the older one and here neither is.
func TestRegressionWatchIgnoresInterleavedVerdicts(t *testing.T) {
	samples := watchSamples(
		watchMeasurement{"1.1.0", "PASS", day1},
		watchMeasurement{"1.1.0", "FAIL", day2},
		watchMeasurement{"1.1.0", "PASS", day3},
	)
	if got := watchFor(samples, "1.1.0"); len(got) != 0 {
		t.Fatalf("an interleaved coordinate was reported as a change over time: %+v", got)
	}
	// Two verdicts at the same instant have no order either.
	same := watchSamples(
		watchMeasurement{"1.1.0", "PASS", day1},
		watchMeasurement{"1.1.0", "FAIL", day1},
	)
	if got := watchFor(same, "1.1.0"); len(got) != 0 {
		t.Fatalf("two simultaneous verdicts were ordered: %+v", got)
	}
}

// A receipt without a recorded time cannot be placed before or after any
// other. An unknown dimension yields no claim.
func TestRegressionWatchRefusesUndatedEvidence(t *testing.T) {
	samples := watchSamples(
		watchMeasurement{"1.1.0", "PASS", time.Time{}},
		watchMeasurement{"1.1.0", "FAIL", day3},
	)
	if got := watchFor(samples, "1.1.0"); len(got) != 0 {
		t.Fatalf("ordered an undated receipt: %+v", got)
	}
}

// A coordinate measured one way only has not changed, however many times it
// was measured. The watch is about change, not about failure.
func TestRegressionWatchIsSilentOnAnUnanimousVerdict(t *testing.T) {
	samples := watchSamples(
		watchMeasurement{"1.1.0", "FAIL", day1},
		watchMeasurement{"1.1.0", "FAIL", day3},
		watchMeasurement{"1.2.0", "PASS", day1},
		watchMeasurement{"1.2.0", "PASS", day3},
	)
	if got := regressionWatchFromReceipts(samples); len(got) != 0 {
		t.Fatalf("an unchanged verdict was reported as a change: %+v", got)
	}
}

// The change is only a change under the conditions that measured both sides.
// The same version failing under another adapter or environment is a
// different measurement, not a contradiction of this one.
func TestRegressionWatchNeverCrossesComparisonConditions(t *testing.T) {
	base := func() []sampleData {
		return watchSamples(
			watchMeasurement{"1.1.0", "PASS", day1},
			watchMeasurement{"1.1.0", "FAIL", day3},
		)
	}
	if got := watchFor(base(), "1.1.0"); len(got) != 1 {
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
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			samples := base()
			change(&samples[1])
			if got := watchFor(samples, "1.1.0"); len(got) != 0 {
				t.Fatalf("crossed a difference in %s to report a change: %+v", name, got)
			}
		})
	}
}

// End to end: the builder publishes the watch entry on the coordinate that
// changed, next to the boundary and upgrade candidates it relates to.
func TestBuilderPublishesRegressionWatchOnTheChangedCoordinate(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	caseID := "case:builder-regression-watch"
	env := envNode("esm")
	for i, m := range []watchMeasurement{
		{"1.11.0", "PASS", day1},
		{"1.12.0", "PASS", day1},
		{"1.12.0", "FAIL", day3},
	} {
		purl := "pkg:npm/axios@" + m.version
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
			Status: "PUBLISHED", License: "MIT-0", CreatedAt: m.at,
		}); err != nil {
			t.Fatal(err)
		}
		receipt := domain.VerificationReceipt{
			SchemaVersion: 2, SampleID: sampleID, CaseID: caseID,
			EnvironmentHash: env.Normalize().Hash(), Environment: env,
			Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": m.result},
			ResolvedPackages: []string{purl}, VerifierAdapter: manifest.VerifierAdapter,
			SandboxCapability: domain.CapContainerRun, CreatedAt: m.at.Format(time.RFC3339),
			PeerID: "ed25519:aaaaaaaaaaaaaaaa",
		}
		if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
			ReceiptID: receipt.ReceiptID(), SampleID: sampleID, PeerID: receipt.PeerID,
			ReceiptJSON:    string(domain.MustCanonicalJSON(receipt)),
			ContractResult: m.result, CreatedAt: m.at,
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
		t.Fatalf("snapshot: ok=%v err=%v", ok, err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RegressionWatchCandidates) != 1 {
		t.Fatalf("watch candidates = %+v", snapshot.RegressionWatchCandidates)
	}
	got := snapshot.RegressionWatchCandidates[0]
	if got.Kind != WatchKnownGoodNowFailing || got.CaseID != caseID ||
		got.NearestLowerPassPackage != "pkg:npm/axios@1.11.0" ||
		got.KnownThrough != day1.UTC().Format(time.RFC3339) ||
		got.LatestSince != day3.UTC().Format(time.RFC3339) {
		t.Fatalf("published candidate = %+v", got)
	}
	// The contested release is neither a boundary nor an upgrade target.
	if len(snapshot.RegressionCandidates) != 0 {
		t.Errorf("contested release anchored a boundary: %+v", snapshot.RegressionCandidates)
	}
	raw, _, _ = store.GetSnapshot(ctx, "pkg:npm/axios@1.11.0", "axios.post")
	var lower Snapshot
	if err := json.Unmarshal([]byte(raw), &lower); err != nil {
		t.Fatal(err)
	}
	if len(lower.SafeUpgradeCandidates) != 0 {
		t.Errorf("offered the contested release as a safe upgrade: %+v", lower.SafeUpgradeCandidates)
	}
}
