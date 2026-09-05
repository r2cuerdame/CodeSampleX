package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// axisSeed writes one PUBLIC release, optionally with a passing sample and
// optionally with evidence, into the Fake.
type axisSeed struct {
	ecosystem, name, version string
	proven                   bool
	observations             int
	direct                   bool
}

// axisStore is the slice of a store the completeness scheduler's scenarios
// need. Both the Fake and PG satisfy it, so the parity check below compares
// the two on identical input rather than on two fixtures that drift.
type axisStore interface {
	UpsertPackage(context.Context, PackageRow) error
	IngestBatches(context.Context, []domain.ObservationBatch) (int, []RejectedBatch, error)
	SaveSample(context.Context, SampleRow) error
	SaveReceipt(context.Context, ReceiptRow) error
	DependencyAxisOpen(context.Context, int, int) ([]DependencyAxisWork, error)
	ListAuthoringExpansionCandidates(context.Context, int) ([]WantedRow, error)
}

func seedAxis(t *testing.T, f axisStore, rows []axisSeed) {
	t.Helper()
	ctx := context.Background()
	for _, r := range rows {
		purl := domain.PURL{Ecosystem: r.ecosystem, Name: r.name, Version: r.version}.String()
		if err := f.UpsertPackage(ctx, PackageRow{
			PURL: purl, Ecosystem: r.ecosystem, Name: r.name, Version: r.version,
			Major: r.version[:1], Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
		if r.observations > 0 {
			b := domain.ObservationBatch{
				SchemaVersion: 1, Epoch: "2026-09-01", AnonID: "peer-" + r.name,
				ProjectBucket: "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
				Package:       purl,
				Environment: domain.EnvironmentFingerprint{
					SchemaVersion: 1, Ecosystem: r.ecosystem, OS: "linux", Arch: "amd64",
				},
				Stage: domain.StageProjectCompile, Result: domain.ResultPass,
				ObservationCount: r.observations, Direct: r.direct,
			}
			if accepted, rejected, err := f.IngestBatches(ctx, []domain.ObservationBatch{b}); err != nil ||
				accepted != 1 || len(rejected) != 0 {
				t.Fatalf("ingest %s: accepted=%d rejected=%v err=%v", r.name, accepted, rejected, err)
			}
		}
		if r.proven {
			id := "sha256:proof-" + r.name + "-" + r.version
			if err := f.SaveSample(ctx, SampleRow{
				SampleID: id, ManifestJSON: `{"packages":["` + purl + `"],"symbols":[]}`,
			}); err != nil {
				t.Fatal(err)
			}
			if err := f.SaveReceipt(ctx, ReceiptRow{
				SampleID: id, ReceiptID: "receipt-" + r.name + "-" + r.version,
				PeerID: "peer-" + r.name, EnvHash: "env-" + r.name,
				ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"linux"}}`,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// A coordinate with a passing sample and no dependency answer is the SE- cell
// -- 1,286 of 3,138 releases on production when #87 was measured -- and it was
// invisible to every queue this server had. The authoring queue excludes it BY
// the sample it already has, and nothing else looked at it, so no amount of
// farm running moved it.
func TestTheDependencyAxisIsWorkEvenWhenTheSampleIsDone(t *testing.T) {
	f := NewFake()
	seedAxis(t, f, []axisSeed{{ecosystem: "npm", name: "left-pad", version: "1.3.0", proven: true}})

	work, err := f.DependencyAxisOpen(context.Background(), DependencyAxisMaxAttempts, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 {
		t.Fatalf("dependency axis work = %d rows, want 1: a proven coordinate with no tree is nobody's work", len(work))
	}
	if work[0].Name != "left-pad" || work[0].SampleID != "sha256:proof-left-pad-1.3.0" {
		t.Errorf("work = %+v, want the coordinate and the sample that can answer it", work[0])
	}
}

// An answer takes the coordinate off the board. Without this the scheduler
// would keep asking a question it already has, which is the difference
// between a queue that converges and one that churns.
func TestAnAnsweredDependencyAxisStopsBeingWork(t *testing.T) {
	ctx := context.Background()
	for _, answer := range []string{"tree", "none"} {
		t.Run(answer, func(t *testing.T) {
			f := NewFake()
			seedAxis(t, f, []axisSeed{{ecosystem: "npm", name: "left-pad", version: "1.3.0", proven: true}})
			b := domain.ObservationBatch{
				SchemaVersion: 1, Epoch: "2026-09-01", AnonID: "peer-report",
				ProjectBucket: "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
				Package:       "pkg:npm/left-pad@1.3.0",
				Environment: domain.EnvironmentFingerprint{
					SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
				},
				Stage: domain.StageUsed, Result: domain.ResultPass, ObservationCount: 1,
			}
			if answer == "tree" {
				b.DependsOn = []string{"pkg:npm/tiny@1.0.0"}
			} else {
				b.DependsOnNone = true
			}
			if _, rejected, err := f.IngestBatches(ctx, []domain.ObservationBatch{b}); err != nil || len(rejected) != 0 {
				t.Fatalf("ingest: rejected=%v err=%v", rejected, err)
			}
			work, err := f.DependencyAxisOpen(ctx, DependencyAxisMaxAttempts, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range work {
				if w.Name == "left-pad" {
					t.Fatalf("left-pad is still dependency work after a resolution reported %q: the queue does not converge", answer)
				}
			}
		})
	}
}

// An ecosystem with no scanner in this binary cannot produce a tree, so
// asking for one is work nobody can close. The rule is
// domain.DependencyNotApplicable -- the same sentence the census subtracts by
// and the /gaps page prints -- rather than a second list beside it.
func TestAnUnscannableEcosystemIsNotDependencyWork(t *testing.T) {
	f := NewFake()
	seedAxis(t, f, []axisSeed{
		{ecosystem: "maven", name: "com.example/thing", version: "1.0.0", proven: true},
		{ecosystem: "npm", name: "left-pad", version: "1.3.0", proven: true},
	})

	work, err := f.DependencyAxisOpen(context.Background(), DependencyAxisMaxAttempts, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range work {
		if w.Ecosystem == "maven" {
			t.Fatal("maven was offered dependency work; nothing in this binary can read a maven tree, so the job could never be closed")
		}
	}
	if len(work) != 1 {
		t.Fatalf("work = %d rows, want only the npm one", len(work))
	}
}

// The attempt ceiling is what stops the scheduler spending the fleet forever
// on a coordinate whose tree will not be read. It counts job rows rather than
// failures: a verification that came back without a tree still spent an
// attempt, and a ceiling that only counted failures would never stop.
func TestTheDependencyAxisStopsAskingAfterTheAttemptCeiling(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	seedAxis(t, f, []axisSeed{{ecosystem: "npm", name: "left-pad", version: "1.3.0", proven: true}})
	sampleID := "sha256:proof-left-pad-1.3.0"

	for i := 0; i < DependencyAxisMaxAttempts; i++ {
		work, err := f.DependencyAxisOpen(ctx, DependencyAxisMaxAttempts, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(work) != 1 {
			t.Fatalf("attempt %d: work = %d rows, want 1", i, len(work))
		}
		id, err := f.EnsureCrossJob(ctx, JobRow{SampleID: sampleID, Reason: "cross", Status: "open"})
		if err != nil {
			t.Fatal(err)
		}
		// A live job is already asking the question; a second would take a
		// verifier from a coordinate nobody is asking about.
		open, err := f.DependencyAxisOpen(ctx, DependencyAxisMaxAttempts, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(open) != 0 {
			t.Fatalf("attempt %d: a sample with a live verification was offered again", i)
		}
		if err := f.CompleteJob(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	work, err := f.DependencyAxisOpen(ctx, DependencyAxisMaxAttempts, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 0 {
		t.Fatalf("work = %d rows after %d attempts: the scheduler never stops asking", len(work), DependencyAxisMaxAttempts)
	}
}

// Demand decides the order, so the axis fills where people actually are. A
// chosen sighting counts a thousand carried ones, which is the ratio the
// authoring queue already uses -- one scale, not two.
func TestDependencyAxisWorkRanksByDemand(t *testing.T) {
	f := NewFake()
	seedAxis(t, f, []axisSeed{
		{ecosystem: "npm", name: "quiet", version: "1.0.0", proven: true, observations: 50},
		{ecosystem: "npm", name: "chosen", version: "1.0.0", proven: true, observations: 1, direct: true},
	})

	work, err := f.DependencyAxisOpen(context.Background(), DependencyAxisMaxAttempts, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 2 {
		t.Fatalf("work = %d rows, want 2", len(work))
	}
	if work[0].Name != "chosen" {
		t.Errorf("first = %q, want the package somebody chose: one direct sighting outweighs fifty carried ones", work[0].Name)
	}
}

// One verification reports the whole tree its resolver wrote, so a sample
// declaring several open coordinates closes all of them. Billing the fleet
// once per coordinate would pay three times for one answer.
func TestOneSampleIsOfferedOnceHoweverManyCoordinatesItDeclares(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	seedAxis(t, f, []axisSeed{
		{ecosystem: "npm", name: "a", version: "1.0.0"},
		{ecosystem: "npm", name: "b", version: "2.0.0"},
	})
	if err := f.SaveSample(ctx, SampleRow{
		SampleID:     "sha256:pair",
		ManifestJSON: `{"packages":["pkg:npm/a@1.0.0","pkg:npm/b@2.0.0"],"symbols":[]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveReceipt(ctx, ReceiptRow{
		SampleID: "sha256:pair", ReceiptID: "receipt-pair", PeerID: "peer-pair",
		EnvHash: "env-pair", ContractResult: "PASS",
		ReceiptJSON: `{"environment":{"os":"linux"}}`,
	}); err != nil {
		t.Fatal(err)
	}

	work, err := f.DependencyAxisOpen(ctx, DependencyAxisMaxAttempts, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 {
		t.Fatalf("work = %d rows, want 1: one verification answers every coordinate the sample declares", len(work))
	}
}

// The evidence axis. A PUBLIC release nothing has ever been recorded running
// cannot be reached by any other branch: they all reach a version through an
// evidence row keyed by that exact purl, or through a sibling already proven.
// The census counted these and no queue could hand them out.
func TestACoordinateWithNoEvidenceAtAllIsStillWork(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	seedAxis(t, f, []axisSeed{{ecosystem: "npm", name: "unseen", version: "1.0.0"}})

	rows, err := f.ListAuthoringExpansionCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == "unseen" {
			if r.Kind != "EVIDENCE" {
				t.Errorf("kind = %q, want EVIDENCE: the missing axis is what a reader can act on", r.Kind)
			}
			return
		}
	}
	t.Fatal("a public release with no evidence, no sample and no resolution was offered as no work at all")
}

// Evidence-axis work must never displace work somebody asked for. It scores
// zero and ranks last, so a release nobody has been seen using sits behind
// every coordinate that has been.
func TestEvidenceAxisWorkNeverOutranksMeasuredDemand(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	seedAxis(t, f, []axisSeed{
		{ecosystem: "npm", name: "unseen", version: "1.0.0"},
		{ecosystem: "npm", name: "wanted", version: "1.0.0", observations: 10, direct: true},
	})

	rows, err := f.ListAuthoringExpansionCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	seenWanted := false
	for _, r := range rows {
		if r.Name == "wanted" {
			seenWanted = true
		}
		if r.Name == "unseen" && !seenWanted {
			t.Fatal("evidence-axis work was offered before a coordinate the network watches people use")
		}
	}
	if !seenWanted {
		t.Fatal("the observed coordinate was not offered at all")
	}
}

// Proving the coordinate takes it off the evidence axis, which is what makes
// the queue converge rather than churn.
func TestEvidenceAxisWorkLeavesTheQueueOnceProven(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	seedAxis(t, f, []axisSeed{{ecosystem: "npm", name: "unseen", version: "1.0.0", proven: true}})

	rows, err := f.ListAuthoringExpansionCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == "unseen" && r.Kind == "EVIDENCE" {
			t.Fatal("a proven coordinate is still offered as evidence-axis work")
		}
	}
}

// An EVIDENCE claim asks about the RELEASE, not about one symbol in it, so
// the assignment row is released on submission rather than stamped with the
// sample id.
//
// A row left behind takes its coordinate off the board permanently: the
// `claimable` filter drops anything an assignment has already answered, and
// it cannot tell a coordinate that is genuinely proven from one whose sample
// never passed its verification. That is the state the FINDING rows
// accumulated into on production -- 407 of them, 141 inside a 200-row window,
// three claimable rows left and authoring at zero handouts for five hours.
func TestAnEvidenceClaimHandsItsRowBackOnSubmission(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	f := NewFake()
	f.NowFn = func() time.Time { return now }
	seedAxis(t, f, []axisSeed{{ecosystem: "npm", name: "unseen", version: "1.0.0"}})
	if err := f.IssueAuthoringSessions(ctx, []AuthoringSessionRow{{
		TokenHash: "hash-evidence", SessionID: "evidence-writer", Label: "writer",
		IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}

	candidates, err := f.ListAuthoringExpansionCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	var evidence []WantedRow
	for _, c := range candidates {
		if c.Kind == "EVIDENCE" {
			evidence = append(evidence, c)
		}
	}
	if len(evidence) == 0 {
		t.Fatal("no evidence-axis candidate to claim")
	}
	work, ok, err := f.ClaimAuthoringWork(ctx, "evidence-writer", evidence, now, now.Add(time.Hour))
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if work.Kind != "EVIDENCE" {
		t.Fatalf("claimed kind = %q, want EVIDENCE", work.Kind)
	}
	attached, err := f.AttachAuthoringWorkSample(ctx, "evidence-writer", work, "sha256:written", now)
	if err != nil || !attached {
		t.Fatalf("attach: attached=%v err=%v", attached, err)
	}

	again, err := f.ListAuthoringExpansionCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range again {
		if c.Name == "unseen" && c.Kind == "EVIDENCE" {
			return
		}
	}
	t.Fatal("the coordinate left the board on submission; nothing can re-offer it if the verification never passes")
}
