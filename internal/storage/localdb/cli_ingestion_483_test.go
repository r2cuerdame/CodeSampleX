package localdb

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Regression tests for the storage symptoms consolidated under #483.

var linuxCLIEnv = domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "linux", Arch: "x64"}

func recordArgv(t *testing.T, db *DB, argv []string, prov domain.ExperienceProvenance, result domain.Result) domain.CLIExperienceCoordinate {
	t.Helper()
	coord := domain.ParseCLICommand(argv, linuxCLIEnv)
	coord.ToolVersion = "2.45.0"
	obs := cliObs(coord, result, "2026-09-20T00:00:00Z")
	obs.Provenance = prov
	if err := db.RecordCLIExperienceObservation(context.Background(), obs); err != nil {
		t.Fatal(err)
	}
	return coord
}

// A recorded command with placeholder arguments is found by its own
// coordinate (#303).
func TestQueryCLIExperienceRecallsPlaceholderArguments(t *testing.T) {
	db := openTemp(t)
	for _, argv := range [][]string{
		{"git", "checkout", "-b", "feature/my-branch"},
		{"git", "clone", "https://github.com/cli/cli"},
		{"git", "show", "4b055b1fd28167f77fb5c554e5eea49b0e4e7377"},
		{"git", "-C", "/tmp/repo", "status"},
	} {
		coord := recordArgv(t, db, argv, domain.ProvenanceField, domain.ResultPass)
		summary, err := db.QueryCLIExperience(context.Background(), coord)
		if err != nil {
			t.Fatal(err)
		}
		if summary.Status != "OBSERVED_PASS" || summary.FieldPassCount != 1 {
			t.Errorf("%v (%s %s): status %s, field passes %d; want OBSERVED_PASS with 1",
				argv, coord.Subcommand, coord.ArgsPattern, summary.Status, summary.FieldPassCount)
		}
	}
}

// A row with no args does not inherit the query's args (#307).
func TestQueryCLIExperienceRowsDoNotInheritTheQuery(t *testing.T) {
	db := openTemp(t)
	recordArgv(t, db, []string{"git", "commit"}, domain.ProvenanceField, domain.ResultPass)
	withMessage := recordArgv(t, db, []string{"git", "commit", "-m", "msg"}, domain.ProvenanceField, domain.ResultFail)

	summary, err := db.QueryCLIExperience(context.Background(), withMessage)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FieldPassCount != 0 || summary.FieldFailCount != 1 || summary.Status != "OBSERVED_FAIL" {
		t.Fatalf("git commit -m: %s pass=%d fail=%d; the bare `git commit` PASS was counted as `-m`",
			summary.Status, summary.FieldPassCount, summary.FieldFailCount)
	}
}

// A field run whose command mentions farm stays field (#314).
func TestQueryCLIExperienceKeepsFieldProvenanceForFarmWords(t *testing.T) {
	db := openTemp(t)
	for _, argv := range [][]string{
		{"git", "checkout", "farm"},
		{"npm", "run", "farm"},
		{"go", "test", "./farm/..."},
	} {
		coord := recordArgv(t, db, argv, domain.ProvenanceField, domain.ResultPass)
		summary, err := db.QueryCLIExperience(context.Background(), coord)
		if err != nil {
			t.Fatal(err)
		}
		if summary.FieldPassCount != 1 || summary.FarmPassCount != 0 {
			t.Errorf("%v: field=%d farm=%d; a field run was attributed to the farm",
				argv, summary.FieldPassCount, summary.FarmPassCount)
		}
	}
	// A real farm row is still farm.
	coord := recordArgv(t, db, []string{"git", "status"}, domain.ProvenanceFarm, domain.ResultPass)
	summary, err := db.QueryCLIExperience(context.Background(), coord)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FarmPassCount != 1 || summary.FieldPassCount != 0 {
		t.Errorf("farm row: field=%d farm=%d, want farm 1", summary.FieldPassCount, summary.FarmPassCount)
	}
}

// Execution evidence keeps its environment even when the fingerprint cache
// row is missing, and writes that row itself (#353).
func TestListCLIExecutionEvidenceKeepsTheCoordinateEnvironment(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	coord := domain.CLIExperienceCoordinate{
		Tool: "git", ToolVersion: "2.55.0", Subcommand: "status",
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "windows", Arch: "amd64"},
	}
	exit1 := 1
	obs := domain.CLIExperienceObservation{
		Coordinate: coord, Provenance: domain.ProvenanceField, Result: domain.ResultFail,
		Termination:     domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit1},
		EvidenceQuality: domain.EvidenceComplete, ObservedAt: "2026-09-11T00:00:00Z",
	}

	// The execution row writes its own environment in its transaction.
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordCLIExecutionEvidence(ctx, tx, obs); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.GetEnvironment(ctx, coord.Canonical().Environment.Hash()); err != nil || !found {
		t.Fatalf("recordCLIExecutionEvidence left no environment row: found=%v err=%v", found, err)
	}

	// A missing cache row falls back to the matched coordinate.
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM environments WHERE hash = ?`, coord.Canonical().Environment.Hash()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListCLIExecutionEvidence(ctx, coord, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.Coordinate.Environment.OS != "windows" || got.Coordinate.Environment.Arch != "amd64" {
		t.Fatalf("environment = %+v, want the coordinate's windows/amd64", got.Coordinate.Environment)
	}
	if got.Coordinate.CoordinateID() != coord.CoordinateID() {
		t.Errorf("coordinate id %s != queried %s", got.Coordinate.CoordinateID(), coord.CoordinateID())
	}
	if got.EvidenceID() != got.ID {
		t.Errorf("EvidenceID() %s != stored %s", got.EvidenceID(), got.ID)
	}
}
