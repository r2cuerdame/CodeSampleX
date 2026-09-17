package localdb

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func cliObs(coord domain.CLIExperienceCoordinate, result domain.Result, observedAt string) domain.CLIExperienceObservation {
	code := 0
	if result == domain.ResultFail {
		code = 1
	}
	return domain.CLIExperienceObservation{
		Coordinate:  coord,
		Provenance:  domain.ProvenanceField,
		Result:      result,
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code},
		ObservedAt:  observedAt,
		Count:       1,
	}
}

// Two recordings of `docker compose up -d --build` and `docker compose up
// --build -d` are two coordinates (the args pattern keeps order) and one
// subject. The subject is what a caller addresses, so both rows answer.
func TestCLIExecutionEvidenceIsAddressableBySubject(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	env := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "linux", Arch: "x64"}
	argvA := []string{"docker", "compose", "up", "-d", "--build"}
	argvB := []string{"docker", "compose", "up", "--build", "-d"}
	argvOther := []string{"docker", "compose", "down"}

	for i, argv := range [][]string{argvA, argvB, argvOther} {
		coord := domain.ParseCLICommand(argv, env)
		coord.ToolVersion = "27.0.1"
		coord.Shell = "bash"
		if err := db.RecordCLIExperienceObservation(ctx, cliObs(coord, domain.ResultPass, "2026-09-1"+string(rune('0'+i))+"T00:00:00Z")); err != nil {
			t.Fatal(err)
		}
	}
	subject := domain.CLISubjectFromArgv(argvA, env, "27.0.1", "bash")
	rows, err := db.ListCLIExecutionEvidenceBySubject(ctx, subject, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows by subject = %d, want 2 (both option orders): %+v", len(rows), rows)
	}
	for _, row := range rows {
		if got := row.Subject().SubjectID(); got != subject.SubjectID() {
			t.Errorf("row subject %s != asked %s", got, subject.SubjectID())
		}
	}
}

// A database written before subject_id existed gains it at open time from
// the columns it already had, so old evidence is addressable too.
func TestSubjectIDBackfillsRowsRecordedBeforeTheColumnExisted(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	env := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "x64"}
	coord := domain.ParseCLICommand([]string{"git", "worktree", "add", "../w"}, env)
	coord.ToolVersion = "2.46.0"
	coord.Shell = "bash"
	if err := db.RecordCLIExperienceObservation(ctx, cliObs(coord, domain.ResultPass, "2026-09-10T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-column row: blank the subject id the way an old
	// database looks after ALTER TABLE ADD COLUMN.
	if _, err := db.sql.ExecContext(ctx, `UPDATE cli_execution_evidence SET subject_id = ''`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListCLIExecutionEvidenceBySubject(ctx, coord.Subject(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows after backfill = %d, want 1", len(rows))
	}
}

// Asking about a subject answers with the exact rows counted and every
// adaptable row named with its delta — the same tool and command on another
// OS or version is evidence about this command somewhere else, and a caller
// weighs that delta, so it is listed, never summed in.
func TestQueryCLISubjectExperienceSeparatesExactFromAdaptable(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	linux := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "x64"}
	windows := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "windows", Arch: "x64"}
	argv := []string{"gh", "workflow", "run", "ci.yml"}

	exact := domain.ParseCLICommand(argv, linux)
	exact.ToolVersion, exact.Shell = "2.40.0", "bash"
	onWindows := domain.ParseCLICommand(argv, windows)
	onWindows.ToolVersion, onWindows.Shell = "2.40.0", "pwsh"
	olderVersion := domain.ParseCLICommand(argv, linux)
	olderVersion.ToolVersion, olderVersion.Shell = "2.30.0", "bash"
	otherCommand := domain.ParseCLICommand([]string{"gh", "workflow", "list"}, linux)
	otherCommand.ToolVersion, otherCommand.Shell = "2.40.0", "bash"

	for _, rec := range []struct {
		coord  domain.CLIExperienceCoordinate
		result domain.Result
	}{
		{exact, domain.ResultPass}, {exact, domain.ResultPass},
		{onWindows, domain.ResultFail}, {olderVersion, domain.ResultFail}, {otherCommand, domain.ResultPass},
	} {
		if err := db.RecordCLIExperienceObservation(ctx, cliObs(rec.coord, rec.result, "2026-09-10T00:00:00Z")); err != nil {
			t.Fatal(err)
		}
	}

	subject := domain.CLISubjectFromArgv(argv, linux, "2.40.0", "bash")
	summary, err := db.QueryCLISubjectExperience(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Subject == nil || summary.Subject.SubjectID() != subject.SubjectID() {
		t.Fatalf("summary does not name the subject asked: %+v", summary.Subject)
	}
	if summary.SubjectRef != subject.Ref() {
		t.Errorf("subjectRef = %q, want %q", summary.SubjectRef, subject.Ref())
	}
	if summary.FieldPassCount != 2 || summary.FieldFailCount != 0 {
		t.Errorf("exact tally = %d pass / %d fail, want 2 / 0 (adaptable rows must not be summed)", summary.FieldPassCount, summary.FieldFailCount)
	}
	if summary.Status != "OBSERVED_PASS" {
		t.Errorf("status = %q, want OBSERVED_PASS", summary.Status)
	}
	if len(summary.Adaptable) != 2 {
		t.Fatalf("adaptable = %d rows, want 2 (windows, older version): %+v", len(summary.Adaptable), summary.Adaptable)
	}
	seen := map[string]bool{}
	for _, a := range summary.Adaptable {
		if a.Match.Grade != domain.GradeAdaptationRequired {
			t.Errorf("adaptable row graded %s", a.Match.Grade)
		}
		for _, d := range a.Match.Different {
			seen[d] = true
		}
		if a.Observation.Result != domain.ResultFail {
			t.Errorf("adaptable row lost its outcome: %+v", a.Observation)
		}
	}
	for _, want := range []string{"os", "shell", "toolVersion"} {
		if !seen[want] {
			t.Errorf("no adaptable row named %q as different: %+v", want, summary.Adaptable)
		}
	}
}
