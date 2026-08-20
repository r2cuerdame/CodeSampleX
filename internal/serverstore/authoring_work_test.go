package serverstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestAuthoringWorkLeasesWantedWithoutDuplicateWriters(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	candidates := []WantedRow{
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post", Asks: 9},
		{Ecosystem: "pypi", Name: "pandas", Version: "3.0.5", Symbol: "pandas.merge", Asks: 4},
	}
	first, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", candidates, now, now.Add(24*time.Hour))
	if err != nil || !ok || first.Name != "axios" {
		t.Fatalf("first claim = %+v ok=%v err=%v", first, ok, err)
	}
	again, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", candidates, now.Add(time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || again.Name != first.Name || !again.ClaimedAt.Equal(first.ClaimedAt) {
		t.Fatalf("same writer claim changed = %+v ok=%v err=%v", again, ok, err)
	}
	second, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-b", candidates, now, now.Add(24*time.Hour))
	if err != nil || !ok || second.Name != "pandas" {
		t.Fatalf("duplicate target assigned = %+v ok=%v err=%v", second, ok, err)
	}
}

func TestAuthoringWorkReleasesLeaseMissingFromCompatibleCandidates(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	windows := []WantedRow{{Ecosystem: "golang", Name: "github.com/Microsoft/go-winio", Version: "0.6.2", Symbol: "ListenPipe", Kind: "FINDING", TargetOS: "windows"}}
	if claimed, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", windows, now, now.Add(24*time.Hour)); err != nil || !ok || claimed.Name == "" {
		t.Fatalf("windows claim = %+v ok=%v err=%v", claimed, ok, err)
	}
	linux := []WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post", Kind: "EXPANSION", TargetOS: "linux"}}
	reassigned, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", linux, now.Add(time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || reassigned.Name != "axios" {
		t.Fatalf("reassigned = %+v ok=%v err=%v", reassigned, ok, err)
	}
	other, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-b", windows, now.Add(time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || other.Name != "github.com/Microsoft/go-winio" {
		t.Fatalf("released target unavailable = %+v ok=%v err=%v", other, ok, err)
	}
}

func TestAuthoringExpansionRanksFailureThenObservedCoverage(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }
	batch := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-19", AnonID: "workerbucket", ProjectBucket: "projectbucket",
		Package: "pkg:npm/axios@1.12.0", Symbol: "axios.post", SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64", Runtime: "node", RuntimeVersion: "22.18"},
		Stage:       domain.StageProjectCompile, Result: domain.ResultFail, ErrorFingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ErrorCode: "ERR_TEST", ObservationCount: 17,
	}
	if accepted, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{batch}); err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("ingest = %d rejected=%v err=%v", accepted, rejected, err)
	}
	secondary := batch
	secondary.AnonID = "otherworker"
	secondary.ProjectBucket = "otherproject"
	secondary.Symbol = "axios.get"
	secondary.Result = domain.ResultPass
	secondary.ErrorFingerprint = ""
	secondary.ErrorCode = ""
	secondary.ObservationCount = 5
	if accepted, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{secondary}); err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("secondary ingest = %d rejected=%v err=%v", accepted, rejected, err)
	}
	if err := store.UpsertPackage(ctx, PackageRow{PURL: batch.Package, Ecosystem: "npm", Name: "axios", Version: "1.12.0", Publicness: "PUBLIC"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFailureCluster(ctx, ClusterRow{
		Ecosystem: "npm", PackageName: "axios", Symbol: "axios.post", Stage: "PROJECT_COMPILE",
		ErrorFingerprint: batch.ErrorFingerprint, ErrorCode: batch.ErrorCode, ObservationCount: 17,
		VersionsJSON: `["1.12.0"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSample(ctx, SampleRow{SampleID: "sha256:linux-proof", ManifestJSON: `{"packages":["pkg:npm/axios@1.12.0"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, ReceiptRow{ReceiptID: "receipt-linux-proof", SampleID: "sha256:linux-proof", ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"windows"}}`}); err != nil {
		t.Fatal(err)
	}

	// The package-level row this used to expect is gone on purpose. The
	// fixture's sample already proves axios@1.12.0 on windows, and offering
	// that same coordinate again is what put 201 samples on one purl in
	// production. Asserting it here is what kept the bug alive, so the
	// assertion is inverted rather than relaxed.
	candidates, err := store.ListAuthoringExpansionCandidates(ctx, 10)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates = %+v err=%v", candidates, err)
	}
	if candidates[0].Kind != "FINDING" || candidates[0].Symbol != "axios.post" || candidates[0].Score != 17 {
		t.Fatalf("first candidate = %+v", candidates[0])
	}
	if candidates[1].Kind != "EXPANSION" || candidates[1].Symbol != "axios.get" || candidates[1].Score != 5 {
		t.Fatalf("symbol expansion = %+v", candidates[1])
	}
	for _, c := range candidates {
		if c.Symbol == "" {
			t.Fatalf("a package-level coordinate already proven on windows was offered: %+v", c)
		}
	}

	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-finding", candidates, now, now.Add(24*time.Hour))
	if err != nil || !ok || work.Kind != "FINDING" || work.Score != 17 {
		t.Fatalf("claim = %+v ok=%v err=%v", work, ok, err)
	}
	remaining, err := store.ListAuthoringExpansionCandidates(ctx, 10)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("remaining = %+v err=%v", remaining, err)
	}
	next, ok, err := store.ClaimAuthoringWork(ctx, "writer-expansion", remaining, now, now.Add(24*time.Hour))
	if err != nil || !ok || next.Kind != "EXPANSION" || next.Symbol != "axios.get" {
		t.Fatalf("second claim = %+v ok=%v err=%v", next, ok, err)
	}
}

func TestPackageExpansionReopensAfterACompletedDraft(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	candidate := WantedRow{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Kind: "EXPANSION", Score: 20, TargetOS: "linux"}
	work, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", []WantedRow{candidate}, now, now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("first claim = %+v ok=%v err=%v", work, ok, err)
	}
	if attached, err := store.AttachAuthoringWorkSample(t.Context(), "writer-a", work, "sha256:package-expansion", now.Add(time.Minute)); err != nil || !attached {
		t.Fatalf("attach = %v err=%v", attached, err)
	}
	reopened, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-b", []WantedRow{candidate}, now.Add(2*time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || reopened.Name != "axios" {
		t.Fatalf("reopened = %+v ok=%v err=%v", reopened, ok, err)
	}
}

func TestFailedCrossReleasesWantedForAnotherSampleWriter(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	candidates := []WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post", Asks: 9}}
	work, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", candidates, now, now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("claim = %+v %v %v", work, ok, err)
	}
	const sampleID = "sha256:wanted-attempt"
	if attached, err := store.AttachAuthoringWorkSample(t.Context(), "writer-a", work, sampleID, now.Add(time.Hour)); err != nil || !attached {
		t.Fatalf("attach = %v %v", attached, err)
	}
	_ = store.SaveSample(t.Context(), SampleRow{SampleID: sampleID, ManifestJSON: `{}`, Status: "DRAFT", Quarantined: true})
	jobID, _ := store.CreateJob(t.Context(), JobRow{SampleID: sampleID, Reason: "cross"})
	const peer = "ed25519:0123456789abcdef"
	_, _ = store.ClaimJob(t.Context(), jobID, peer)
	if accepted, err := store.SaveReceiptForJob(t.Context(), ReceiptRow{
		ReceiptID: "sha256:failed-wanted-attempt", SampleID: sampleID, PeerID: peer, ContractResult: "FAIL",
	}, jobID); err != nil || !accepted {
		t.Fatalf("fail receipt = %v %v", accepted, err)
	}
	retry, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-b", candidates, now.Add(2*time.Hour), now.Add(26*time.Hour))
	if err != nil || !ok || retry.Name != "axios" {
		t.Fatalf("failed target was not released = %+v %v %v", retry, ok, err)
	}
}

// A version nobody has measured yet is exactly the blank cell the matrix
// shows. It only becomes reachable work if the candidate list can name a
// sibling version that carries no evidence of its own, so this asserts the
// unmeasured siblings appear at all -- ordering is a separate concern.
func TestAuthoringExpansionOffersUnmeasuredSiblingVersions(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }

	// Only 3.0.0 was ever observed or proven. 1.0.0 and 2.0.0 are known
	// public releases with no evidence row of their own.
	batch := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-19", AnonID: "workerbucket", ProjectBucket: "projectbucket",
		Package: "pkg:npm/axios@3.0.0", Symbol: "axios.get", SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64", Runtime: "node", RuntimeVersion: "22.18"},
		Stage:       domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 9,
	}
	if accepted, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{batch}); err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("ingest = %d rejected=%v err=%v", accepted, rejected, err)
	}
	for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		if err := store.UpsertPackage(ctx, PackageRow{
			PURL: "pkg:npm/axios@" + v, Ecosystem: "npm", Name: "axios", Version: v, Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveSample(ctx, SampleRow{SampleID: "sha256:proof", ManifestJSON: `{"packages":["pkg:npm/axios@3.0.0"],"symbols":["axios.get"]}`}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, ReceiptRow{ReceiptID: "receipt-proof", SampleID: "sha256:proof", ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"linux"}}`}); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.ListAuthoringExpansionCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1.0.0", "2.0.0"} {
		found := false
		for _, c := range candidates {
			if c.Version == want && c.Symbol == "" {
				found = true
				if c.Kind != "EXPANSION" {
					t.Errorf("sibling %s kind = %q, want EXPANSION", want, c.Kind)
				}
				// The verifier only claims work whose target OS it can
				// actually execute, so an untargeted row is unclaimable.
				if c.TargetOS != "linux" {
					t.Errorf("sibling %s targetOS = %q, want linux", want, c.TargetOS)
				}
			}
		}
		if !found {
			t.Errorf("version %s missing from candidates; got %+v", want, candidates)
		}
	}
}

// Ranking by score alone deepens whatever version already carries the most
// evidence: it hands out that version's second and third job before another
// version has been measured once. Breadth across versions is the whole point
// of the matrix, so every version earns its first job before any version
// earns its second.
func TestAuthoringExpansionSpreadsAcrossVersionsBeforeDeepening(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }

	base := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-19", ProjectBucket: "projectbucket",
		Package: "pkg:npm/axios@3.0.0", SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64", Runtime: "node", RuntimeVersion: "22.18"},
		Stage:       domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 40,
	}
	for i, symbol := range []string{"axios.get", "axios.put", "axios.patch"} {
		b := base
		b.Symbol = symbol
		b.AnonID = "worker" + string(rune('a'+i))
		if accepted, _, err := store.IngestBatches(ctx, []domain.ObservationBatch{b}); err != nil || accepted != 1 {
			t.Fatalf("ingest %s = %d err=%v", symbol, accepted, err)
		}
	}
	for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		if err := store.UpsertPackage(ctx, PackageRow{
			PURL: "pkg:npm/axios@" + v, Ecosystem: "npm", Name: "axios", Version: v, Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A finding is 3.0.0's first job; its package-level expansion is only its
	// second. Without a version-aware order that second job outranks the
	// untouched siblings purely because 3.0.0 carries more evidence.
	if err := store.UpsertFailureCluster(ctx, ClusterRow{
		Ecosystem: "npm", PackageName: "axios", Symbol: "axios.post", Stage: "PROJECT_COMPILE",
		ErrorFingerprint: "sha256:" + strings.Repeat("b", 64), ErrorCode: "ERR_T", ObservationCount: 31,
		VersionsJSON: `["3.0.0"]`, EnvSummaryJSON: `{"os":"linux"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSample(ctx, SampleRow{SampleID: "sha256:proof", ManifestJSON: `{"packages":["pkg:npm/axios@3.0.0"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, ReceiptRow{ReceiptID: "receipt-proof", SampleID: "sha256:proof", ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"linux"}}`}); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.ListAuthoringExpansionCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	versions := []string{"1.0.0", "2.0.0", "3.0.0"}
	seen := map[string]int{}
	for i, c := range candidates {
		seen[c.Version]++
		if seen[c.Version] < 2 {
			continue
		}
		for _, v := range versions {
			if seen[v] == 0 {
				t.Fatalf("candidate %d is job #%d for %s while %s has none yet; order=%s",
					i, seen[c.Version], c.Version, v, formatCandidateOrder(candidates))
			}
		}
	}
	for _, v := range versions {
		if seen[v] == 0 {
			t.Errorf("version %s never offered; order=%s", v, formatCandidateOrder(candidates))
		}
	}
}

func formatCandidateOrder(rows []WantedRow) string {
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		symbol := r.Symbol
		if symbol == "" {
			symbol = "(package)"
		}
		fmt.Fprintf(&b, "%s/%s:%s", r.Version, symbol, r.Kind)
	}
	return b.String()
}

// A package's unmeasured siblings are all first jobs, so they all land at
// version_depth 1 -- and a package with a long release history therefore fills
// the whole candidate window with score-0 rows, pushing every other package's
// real work past the limit. One recently-crawled package must not be able to
// hold the entire authoring fleet hostage.
func TestAuthoringExpansionCapsSiblingsPerPackage(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }

	// bigpkg: proven at 1.0.0, plus 60 public releases nobody has measured.
	big := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-19", AnonID: "bigpeer", ProjectBucket: "bigproject",
		Package: "pkg:npm/bigpkg@1.0.0", Symbol: "big.call", SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64", Runtime: "node", RuntimeVersion: "22.18"},
		Stage:       domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 3,
	}
	if accepted, _, err := store.IngestBatches(ctx, []domain.ObservationBatch{big}); err != nil || accepted != 1 {
		t.Fatalf("big ingest = %d err=%v", accepted, err)
	}
	for i := 1; i <= 60; i++ {
		v := fmt.Sprintf("%d.0.0", i)
		if err := store.UpsertPackage(ctx, PackageRow{
			PURL: "pkg:npm/bigpkg@" + v, Ecosystem: "npm", Name: "bigpkg", Version: v, Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveSample(ctx, SampleRow{SampleID: "sha256:big-proof", ManifestJSON: `{"packages":["pkg:npm/bigpkg@1.0.0"],"symbols":["big.call"]}`}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, ReceiptRow{ReceiptID: "receipt-big", SampleID: "sha256:big-proof", ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"linux"}}`}); err != nil {
		t.Fatal(err)
	}

	// smallpkg: one heavily observed symbol nobody has answered. This is real,
	// high-value work that must stay reachable.
	small := big
	small.Package = "pkg:npm/smallpkg@1.0.0"
	small.Symbol = "small.wanted"
	small.AnonID = "smallpeer"
	small.ProjectBucket = "smallproject"
	small.ObservationCount = 5000
	if accepted, _, err := store.IngestBatches(ctx, []domain.ObservationBatch{small}); err != nil || accepted != 1 {
		t.Fatalf("small ingest = %d err=%v", accepted, err)
	}
	if err := store.UpsertPackage(ctx, PackageRow{
		PURL: "pkg:npm/smallpkg@1.0.0", Ecosystem: "npm", Name: "smallpkg", Version: "1.0.0", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.ListAuthoringExpansionCandidates(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	bigSiblings := 0
	smallReachable := false
	for _, c := range candidates {
		if c.Name == "bigpkg" && c.Version != "1.0.0" {
			bigSiblings++
		}
		if c.Name == "smallpkg" {
			smallReachable = true
		}
	}
	if bigSiblings > authoringSiblingVersionsPerPackage {
		t.Errorf("bigpkg contributed %d sibling rows, cap is %d; order=%s",
			bigSiblings, authoringSiblingVersionsPerPackage, formatCandidateOrder(candidates))
	}
	if !smallReachable {
		t.Errorf("smallpkg's work was pushed out of the window entirely; order=%s",
			formatCandidateOrder(candidates))
	}
}

// Package-level expansion exists to get a package-level sample for an
// environment that has evidence but no proof yet. It was generated FROM the
// already-proven (purl, os) pairs instead of excluded BY them, and the symbol
// filter never applied to package-level rows at all — so a package proven on
// linux was offered for linux again, and again, forever. In production that
// produced 201 verified samples for one coordinate, all on the same OS, and
// 37% of the whole corpus was redundant.
func TestAuthoringExpansionStopsOfferingAProvenPackageLevelCoordinate(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }

	batch := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-19", AnonID: "peer", ProjectBucket: "proj",
		Package: "pkg:npm/proven@1.0.0", Symbol: "proven.call", SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64", Runtime: "node", RuntimeVersion: "22.18"},
		Stage:       domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 12,
	}
	if accepted, _, err := store.IngestBatches(ctx, []domain.ObservationBatch{batch}); err != nil || accepted != 1 {
		t.Fatalf("ingest = %d err=%v", accepted, err)
	}
	if err := store.UpsertPackage(ctx, PackageRow{
		PURL: "pkg:npm/proven@1.0.0", Ecosystem: "npm", Name: "proven", Version: "1.0.0", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
	// A package-level sample already proves this package on linux.
	if err := store.SaveSample(ctx, SampleRow{SampleID: "sha256:pkglevel", ManifestJSON: `{"packages":["pkg:npm/proven@1.0.0"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, ReceiptRow{ReceiptID: "receipt-pkglevel", SampleID: "sha256:pkglevel", ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"linux"}}`}); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.ListAuthoringExpansionCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.Name == "proven" && c.Version == "1.0.0" && c.Symbol == "" && c.TargetOS == "linux" {
			t.Fatalf("a package-level coordinate already proven on linux was offered again: order=%s",
				formatCandidateOrder(candidates))
		}
	}
	// The symbol nobody has answered is still work worth doing.
	foundSymbol := false
	for _, c := range candidates {
		if c.Symbol == "proven.call" {
			foundSymbol = true
		}
	}
	if !foundSymbol {
		t.Errorf("the unanswered symbol was dropped along with the duplicate: order=%s",
			formatCandidateOrder(candidates))
	}
}

// Revoking a session marked the session and left its claim behind. The lease
// runs 24 hours and the assignment key ignores who holds it, so one worker
// stopped mid-job took its coordinates off the board for a day — for every
// other worker too. Five coordinates were sitting like that in production.
func TestRevokingASessionReleasesItsUnfinishedClaim(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }

	candidates := []WantedRow{{
		Ecosystem: "npm", Name: "left", Version: "1.0.0", Symbol: "left.call",
		Kind: "EXPANSION", Score: 9, TargetOS: "linux",
	}}
	if err := store.IssueAuthoringSessions(ctx, []AuthoringSessionRow{{
		TokenHash: "hash-doomed", SessionID: "doomed", Label: "doomed", Model: "agy",
		Reasoning: "auto", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	work, ok, err := store.ClaimAuthoringWork(ctx, "doomed", candidates, now, now.Add(24*time.Hour))
	if err != nil || !ok || work.Symbol != "left.call" {
		t.Fatalf("claim = %+v ok=%v err=%v", work, ok, err)
	}

	if revoked, err := store.RevokeAuthoringSession(ctx, "doomed", now.Add(time.Minute)); err != nil || !revoked {
		t.Fatalf("revoke = %v err=%v", revoked, err)
	}

	// A minute later, long before the 24h lease, somebody else must be able
	// to pick the abandoned work up.
	later := now.Add(2 * time.Minute)
	next, ok, err := store.ClaimAuthoringWork(ctx, "successor", candidates, later, later.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || next.Symbol != "left.call" {
		t.Errorf("the abandoned coordinate is still locked: got %+v ok=%v", next, ok)
	}
}

// Revoking is not the only way a session stops. One that simply quits
// refreshing idles out, and its claim used to sit for the full 24 hours
// exactly as a revoked one did — production had two coordinates locked that
// way, by sessions that had been dead for four hours.
func TestClaimReleasesWorkHeldByASessionThatIdledOut(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }

	candidates := []WantedRow{{
		Ecosystem: "npm", Name: "stranded", Version: "1.0.0", Symbol: "stranded.call",
		Kind: "EXPANSION", Score: 9, TargetOS: "linux",
	}}
	if err := store.IssueAuthoringSessions(ctx, []AuthoringSessionRow{{
		TokenHash: "hash-quitter", SessionID: "quitter", Label: "quitter", Model: "agy",
		Reasoning: "auto", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimAuthoringWork(ctx, "quitter", candidates, now, now.Add(24*time.Hour)); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	// Two hours on: the session idled out an hour ago, and its lease still
	// has 22 hours to run.
	later := now.Add(2 * time.Hour)
	next, ok, err := store.ClaimAuthoringWork(ctx, "successor", candidates, later, later.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || next.Symbol != "stranded.call" {
		t.Errorf("work held by an idled-out session is still locked: got %+v ok=%v", next, ok)
	}
}

// A coordinate whose draft is waiting for cross-verification is not proven
// yet, so it stayed a candidate and a second worker claimed it minutes after
// the first submitted. With one worker that rarely showed; with two it
// produced six duplicate coordinates in six hours, each pair three or four
// minutes apart. Work in flight is work done.
func TestAuthoringExpansionSkipsCoordinatesWithADraftInFlight(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }

	batch := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-19", AnonID: "peer", ProjectBucket: "proj",
		Package: "pkg:npm/inflight@1.0.0", Symbol: "inflight.call", SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64", Runtime: "node", RuntimeVersion: "22.18"},
		Stage:       domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 30,
	}
	if accepted, _, err := store.IngestBatches(ctx, []domain.ObservationBatch{batch}); err != nil || accepted != 1 {
		t.Fatalf("ingest = %d err=%v", accepted, err)
	}
	if err := store.UpsertPackage(ctx, PackageRow{
		PURL: "pkg:npm/inflight@1.0.0", Ecosystem: "npm", Name: "inflight", Version: "1.0.0", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}

	before, err := store.ListAuthoringExpansionCandidates(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	offered := func(rows []WantedRow) bool {
		for _, c := range rows {
			if c.Name == "inflight" && c.Symbol == "inflight.call" {
				return true
			}
		}
		return false
	}
	if !offered(before) {
		t.Fatalf("the symbol was never offered to begin with: %s", formatCandidateOrder(before))
	}

	// A worker submits a draft for it. The sample is not public yet — it is
	// waiting on an independent verification — so nothing about "proven"
	// changes, and that was exactly the hole.
	if err := store.SaveSample(ctx, SampleRow{
		SampleID: "sha256:inflight-draft", Status: "DRAFT", Quarantined: true,
		ManifestJSON: `{"packages":["pkg:npm/inflight@1.0.0"],"symbols":["inflight.call"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAuthoringDraft(ctx, AuthoringDraftRow{
		SampleID: "sha256:inflight-draft", SessionID: "writer", WorkerLabel: "w1",
		ManifestJSON: `{"packages":["pkg:npm/inflight@1.0.0"],"symbols":["inflight.call"]}`,
		LocalStatus:  "LOCAL_PASS", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	after, err := store.ListAuthoringExpansionCandidates(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if offered(after) {
		t.Errorf("a coordinate with a draft in flight was offered again: %s", formatCandidateOrder(after))
	}
}
