package serverstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The candidate window is finite and the queue is not. A row that can never be
// handed out still costs a slot in it, and production measured on 2026-08-23
// what that costs: of the 200 rows the scheduler produced, 141 were coordinates
// an assignment already answered and 56 were npm platform builds the handler
// drops -- three were claimable. Authoring handouts went 45/h at 15:00 UTC, 3
// at 16:00, and zero for the five hours after that, with 1,810 coverage holes
// on the board the whole time.
//
// An answered coordinate is not "low priority". ClaimAuthoringWork inserts
// ON CONFLICT DO NOTHING against a row nothing ever deletes, so it is
// unclaimable forever -- and it sorts by the observation count that made it
// worth answering first, which is why it lands at the TOP of the window.
func TestAnsweredFindingLeavesTheCandidateWindow(t *testing.T) {
	f := NewFake()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	seedObservedPackage(t, f, "hot", "1.0.0", "windows", 900, true)
	seedObservedPackage(t, f, "cold", "1.0.0", "windows", 1, false)
	seedFailureCluster(t, f, "hot", "1.0.0", 900)

	before := candidateNames(t, f)
	if !containsName(before, "hot") || !containsName(before, "cold") {
		t.Fatalf("both coordinates should start as candidates, got %v", before)
	}

	// A worker takes the finding and submits a sample for it. A symbol-less
	// FINDING assignment keeps its row -- AttachAuthoringWorkSample deletes
	// only EXPANSION and DEPENDENCY -- so the coordinate is unclaimable from
	// here on. Production held 407 of these.
	claimAndAnswer(t, f, "hot", "1.0.0", now)

	after := candidateNames(t, f)
	if containsName(after, "hot") {
		t.Errorf("an answered finding is still a candidate: %v", after)
	}
	if !containsName(after, "cold") {
		t.Errorf("the claimable coordinate fell out of the window: %v", after)
	}
}

// seedFailureCluster records the unexplained failure that makes a coordinate
// FINDING work. Symbol-less, which is what 77% of production's clusters are.
func seedFailureCluster(t *testing.T, f *Fake, name, version string, count int) {
	t.Helper()
	if err := f.UpsertFailureCluster(context.Background(), ClusterRow{
		Ecosystem: "npm", PackageName: name, Symbol: "",
		Stage: "PROJECT_COMPILE", ErrorFingerprint: "fp-" + name, ErrorCode: "ERR_X",
		ObservationCount: int64(count), EnvSummaryJSON: `{"os":"windows"}`,
		VersionsJSON: `["` + version + `"]`,
	}); err != nil {
		t.Fatal(err)
	}
}

// The starvation, reproduced as a loop rather than as a snapshot. A finite
// number of polls has to reach the coordinate at the bottom, because that is
// the only property that separates "ranked last" from "never".
//
// The window here is ten rows against a corpus of seventeen, which is the
// production shape scaled down: the answered findings score highest, so they
// take the whole window and everything real sits just outside it.
func TestEveryClaimableCoordinateIsHandedOutInFinitePolls(t *testing.T) {
	ctx := t.Context()
	f := NewFake()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	const window = 10
	const answered = 12
	for i := 0; i < answered; i++ {
		name := fmt.Sprintf("answered%02d", i)
		seedObservedPackage(t, f, name, "1.0.0", "windows", 900+i, true)
		seedFailureCluster(t, f, name, "1.0.0", 900+i)
		claimAndAnswerAs(t, f, fmt.Sprintf("seed-%02d", i), name, "1.0.0", now)
	}
	// One high-demand coordinate somebody chose, and a tail of carried-only
	// ones nobody did.
	seedObservedPackage(t, f, "chosen", "1.0.0", "windows", 500, true)
	const tail = 4
	for i := 0; i < tail; i++ {
		seedObservedPackage(t, f, fmt.Sprintf("carried%02d", i), "1.0.0", "windows", 3, false)
	}

	handed := map[string]bool{}
	for poll := 0; poll < 200; poll++ {
		session := fmt.Sprintf("s%03d", poll)
		candidates, err := f.ListAuthoringExpansionCandidates(ctx, window)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) == 0 {
			break
		}
		work, ok, err := f.ClaimAuthoringWork(ctx, session, candidates, now, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		handed[work.Name] = true
		answer(t, f, session, work, now)
	}
	for i := 0; i < tail; i++ {
		name := fmt.Sprintf("carried%02d", i)
		if !handed[name] {
			t.Errorf("%s was never handed out in 200 polls; handed=%v", name, handed)
		}
	}
	if !handed["chosen"] {
		t.Errorf("the chosen coordinate was never handed out; handed=%v", handed)
	}
}

// seedObservedPackage records one PUBLIC coordinate observed on targetOS with
// count sightings, chosen or merely carried.
func seedObservedPackage(t *testing.T, store expansionStore, name, version, targetOS string, count int, direct bool) {
	t.Helper()
	ctx := context.Background()
	purl := "pkg:npm/" + name + "@" + version
	if _, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "anon-" + name,
		ProjectBucket: "proj-" + name, Package: purl,
		Stage: domain.StageProjectCompile, Result: domain.ResultPass,
		ObservationCount: count, Direct: direct,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: targetOS, Arch: "x64",
			Runtime: "node", RuntimeVersion: "22",
		},
	}}); err != nil || len(rejected) != 0 {
		t.Fatalf("ingest %s: rejected=%v err=%v", name, rejected, err)
	}
	if err := store.UpsertPackage(ctx, PackageRow{
		PURL: purl, Ecosystem: "npm", Name: name, Version: version,
		Major: version[:1], Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
}

func candidateNames(t *testing.T, f *Fake) []string {
	t.Helper()
	rows, err := f.ListAuthoringExpansionCandidates(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

func containsName(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// claimAndAnswer hands one named coordinate to a session and attaches a sample
// to it, which is the state a completed authoring job leaves behind.
func claimAndAnswer(t *testing.T, f *Fake, name, version string, now time.Time) {
	t.Helper()
	claimAndAnswerAs(t, f, "answering-session", name, version, now)
}

func claimAndAnswerAs(t *testing.T, f *Fake, session, name, version string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	candidates, err := f.ListAuthoringExpansionCandidates(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	picked := make([]WantedRow, 0, 1)
	for _, c := range candidates {
		if c.Name == name && c.Version == version {
			picked = append(picked, c)
		}
	}
	if len(picked) == 0 {
		t.Fatalf("%s@%s was not a candidate", name, version)
	}
	work, ok, err := f.ClaimAuthoringWork(ctx, session, picked, now, now.Add(time.Hour))
	if err != nil || !ok {
		t.Fatalf("claim %s: ok=%v err=%v", name, ok, err)
	}
	answer(t, f, session, work, now)
}

// answer records the sample a worker produced for the work it holds, exactly
// as the submission path does.
func answer(t *testing.T, f *Fake, session string, work AuthoringWorkRow, now time.Time) {
	t.Helper()
	ctx := context.Background()
	purl := domain.PURL{Ecosystem: work.Ecosystem, Name: work.Name, Version: work.Version}.String()
	sampleID := "sha256:answer-" + work.Name + "-" + work.Version + "-" + work.Symbol
	symbols := ""
	if work.Symbol != "" {
		symbols = `"` + work.Symbol + `"`
	}
	if err := f.SaveSample(ctx, SampleRow{
		SampleID:     sampleID,
		ManifestJSON: `{"packages":["` + purl + `"],"symbols":[` + symbols + `]}`,
		Status:       "CROSS_PASS", License: "MIT-0", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveReceipt(ctx, ReceiptRow{
		SampleID: sampleID, ReceiptID: "r-" + sampleID, PeerID: "peer-1",
		ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"linux"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.AttachAuthoringWorkSample(ctx, session, work, sampleID, now); err != nil {
		t.Fatal(err)
	}
}

// completenessStore is the slice of a store the 3-axis scheduler checks need.
// Both the Fake and PG satisfy it.
type completenessStore interface {
	expansionStore
	UpsertFailureCluster(context.Context, ClusterRow) error
	ClaimAuthoringWork(context.Context, string, []WantedRow, time.Time, time.Time) (AuthoringWorkRow, bool, error)
	AttachAuthoringWorkSample(context.Context, string, AuthoringWorkRow, string, time.Time) (bool, error)
}

// The Fake and PostgreSQL have drifted apart on this query twice already, and
// each time a test proved an assignment the server would never make. This one
// replays the exact sequence that emptied production -- observe, cluster,
// claim, attach -- through both and compares what is left on the board.
func TestIntegrationAnsweredFindingLeavesTheWindowInBothStores(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	play := func(t *testing.T, store completenessStore) []string {
		t.Helper()
		ctx := context.Background()
		seedObservedPackage(t, store, "hot", "1.0.0", "windows", 900, true)
		seedObservedPackage(t, store, "cold", "1.0.0", "windows", 1, false)
		if err := store.UpsertFailureCluster(ctx, ClusterRow{
			Ecosystem: "npm", PackageName: "hot", Symbol: "",
			Stage: "PROJECT_COMPILE", ErrorFingerprint: "fp-hot", ErrorCode: "ERR_X",
			ObservationCount: 900, EnvSummaryJSON: `{"os":"windows"}`,
			VersionsJSON: `["1.0.0"]`,
		}); err != nil {
			t.Fatal(err)
		}
		before, err := store.ListAuthoringExpansionCandidates(ctx, 50)
		if err != nil {
			t.Fatal(err)
		}
		var finding []WantedRow
		for _, c := range before {
			if c.Kind == "FINDING" && c.Name == "hot" {
				finding = append(finding, c)
			}
		}
		if len(finding) == 0 {
			t.Fatalf("no finding to answer in %s", formatCandidateOrder(before))
		}
		work, ok, err := store.ClaimAuthoringWork(ctx, "answering-session", finding, now, now.Add(time.Hour))
		if err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		const sampleID = "sha256:answer-hot"
		if err := store.SaveSample(ctx, SampleRow{
			SampleID:     sampleID,
			ManifestJSON: `{"packages":["pkg:npm/hot@1.0.0"],"symbols":[]}`,
			Status:       "CROSS_PASS", License: "MIT-0", CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveReceipt(ctx, ReceiptRow{
			SampleID: sampleID, ReceiptID: "r-answer-hot", PeerID: "peer-1",
			EnvHash: "env-hot", ContractResult: "PASS",
			ReceiptJSON: `{"environment":{"os":"linux"}}`,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AttachAuthoringWorkSample(ctx, "answering-session", work, sampleID, now); err != nil {
			t.Fatal(err)
		}
		after, err := store.ListAuthoringExpansionCandidates(ctx, 50)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(after))
		for _, r := range after {
			out = append(out, candidateLine(r))
		}
		return out
	}

	fake := NewFake()
	fake.NowFn = func() time.Time { return now }
	fakeRows := play(t, fake)
	pgRows := play(t, openTestPG(t))

	for _, row := range fakeRows {
		if strings.Contains(row, "hot@") {
			t.Errorf("fake still offers the answered finding: %v", fakeRows)
			break
		}
	}
	if len(fakeRows) != len(pgRows) {
		t.Fatalf("row count differs: fake=%d pg=%d\n fake: %v\n pg:   %v",
			len(fakeRows), len(pgRows), fakeRows, pgRows)
	}
	for i := range pgRows {
		if fakeRows[i] != pgRows[i] {
			t.Errorf("row %d differs\n  fake: %s\n  pg:   %s", i, fakeRows[i], pgRows[i])
		}
	}
}
