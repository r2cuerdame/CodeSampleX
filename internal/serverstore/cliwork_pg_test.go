package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A CLI claim has to survive the assignment table's kind CHECK. The Fake has
// no constraint to violate, so this is the one test that can see 0049
// missing: without it the INSERT fails and every CLI job the funnel offers
// is refused with "claiming authoring work failed".
func TestIntegrationCLIWorkCanActuallyBeClaimed(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	if err := pg.IssueAuthoringSessions(ctx, []AuthoringSessionRow{{
		TokenHash: "cli-hash", SessionID: "cli-session", Label: "linux-slot1", Model: "agy",
		Reasoning: "auto", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	probe := WantedRow{Ecosystem: "generic", Name: "cli/gh", Version: "",
		Symbol: domain.EncodeCLIWorkSymbol("linux", ""), Kind: "CLI", Axis: AuthoringAxisEvidence, TargetOS: "linux", Score: 9}
	work, found, err := pg.ClaimAuthoringWork(ctx, "cli-session", []WantedRow{probe}, now, now.Add(24*time.Hour))
	if err != nil || !found {
		t.Fatalf("claim: found=%v err=%v", found, err)
	}
	if work.Kind != "CLI" || work.Axis != AuthoringAxisEvidence || work.Name != "cli/gh" || work.Version != "" || work.Symbol != "[linux]" {
		t.Fatalf("claimed %+v", work)
	}
	// Handing it back as unsupported is recorded on the same key.
	released, ok, err := pg.ReportAuthoringOutcome(ctx, "cli-session", AuthoringUnsupportedEnvironment, "farm host has no gh", now.Add(time.Minute))
	if err != nil || !ok || released.Symbol != "[linux]" {
		t.Fatalf("report: ok=%v err=%v released=%+v", ok, err, released)
	}
	state, found, err := pg.AuthoringAttemptState(ctx, "generic", "cli/gh", "", "[linux]")
	if err != nil || !found || state.SessionsMeasuringUnsupported != 1 {
		t.Fatalf("ledger: found=%v err=%v state=%+v", found, err, state)
	}
}

// The live recheck closes a probe once a farm row exists for the tool on
// that OS, and a command gap once any row exists at that exact coordinate --
// while the sibling coordinate at the same purl stays open.
func TestIntegrationCLIWorkCompletenessRecheck(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	code := 0
	batches := []domain.ObservationBatch{{
		SchemaVersion: 2, Epoch: now.Format("2006-01-02"), AnonID: "0123456789abcdef0123456789abcdef",
		ProjectBucket: "0123456789abcdef0123456789abcdef",
		Package:       "pkg:generic/cli/git@2.47.2", Symbol: "farm:--version",
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "linux", Arch: "amd64"},
		Stage:       domain.StageProjectProcess, Result: domain.ResultPass, ObservationCount: 1,
		TerminationKind: domain.TerminationExit, ExitCode: &code,
	}, {
		SchemaVersion: 2, Epoch: now.Format("2006-01-02"), AnonID: "0123456789abcdef0123456789abcdef",
		ProjectBucket: "0123456789abcdef0123456789abcdef",
		Package:       "pkg:generic/cli/git@2.47.2", Symbol: "field:status --short",
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "Linux", Arch: "amd64"},
		Stage:       domain.StageProjectProcess, Result: domain.ResultPass, ObservationCount: 1,
		TerminationKind: domain.TerminationExit, ExitCode: &code,
	}}
	if accepted, rejected, err := pg.IngestBatches(ctx, batches); err != nil || accepted != 2 {
		t.Fatalf("ingest accepted=%d rejected=%+v err=%v", accepted, rejected, err)
	}
	row := func(version, os, command string) WantedRow {
		return WantedRow{Ecosystem: "generic", Name: "cli/git", Version: version, Kind: "CLI",
			Axis: AuthoringAxisEvidence, TargetOS: os, Symbol: domain.EncodeCLIWorkSymbol(os, command)}
	}
	rows := []WantedRow{
		row("", "linux", ""),                          // probe: closed by the farm row
		row("", "windows", ""),                        // probe on another OS: open
		row("2.47.2", "linux", "status --short"),      // observed (field): closed
		row("2.47.2", "linux", "worktree add <path>"), // same purl, other command: open
		row("2.47.2", "windows", "status --short"),    // same command, other OS: open
	}
	open, err := pg.FilterUnobservedCLIWork(ctx, rows, now)
	if err != nil {
		t.Fatal(err)
	}
	fake := NewFake()
	if _, _, err := fake.IngestBatches(ctx, batches); err != nil {
		t.Fatal(err)
	}
	want, _ := fake.FilterUnobservedCLIWork(ctx, rows, now)
	if len(open) != 3 || len(want) != 3 {
		t.Fatalf("pg open=%+v fake open=%+v", open, want)
	}
	for i := range open {
		if open[i].Symbol != want[i].Symbol || open[i].Version != want[i].Version {
			t.Fatalf("row %d: pg=%+v fake=%+v", i, open[i], want[i])
		}
	}
	if open[0].Symbol != "[windows]" || open[1].Symbol != "[linux] worktree add <path>" || open[2].Symbol != "[windows] status --short" {
		t.Fatalf("open = %+v", open)
	}
}
