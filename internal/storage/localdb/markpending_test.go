package localdb

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A drained row returned to the queue must come back exactly as it was. The
// old restore, RecordObservation with count 0, was a full UPSERT keyed on
// whatever the caller had in hand, and a key thinner than the row erased
// the difference (#338).
func TestMarkObservationsPendingMovesOnlyTheFlag(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	exit := 2
	key := ObsKey{
		Epoch: "2026-09-18", PURL: "pkg:npm/vite@6.0.0", EnvHash: "env",
		Stage: domain.StageProjectCompile, Result: domain.ResultFail,
		ErrorFP: "fp", ErrorCode: "E_BUILD",
		TerminationKind: domain.TerminationExit, ExitCode: &exit,
		ErrorSummary: "rollup failed", EvidenceQuality: domain.EvidenceComplete,
		OuterCommand: "npm run build", OuterStage: domain.StageProjectCompile,
		ActualToolchain: "vite", StageEvidence: domain.FailureStageBuildAggregate,
		Direct: true, Coresident: []string{"pkg:npm/vite@5.4.0"},
		DependsOn: []string{"pkg:npm/rollup@4.0.0"},
	}
	if err := db.RecordObservation(ctx, key, 5); err != nil {
		t.Fatal(err)
	}
	before, err := db.PendingObservations(ctx, 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("rows=%+v err=%v", before, err)
	}
	if err := db.MarkObservationsUploaded(ctx, []ObsKey{key}); err != nil {
		t.Fatal(err)
	}
	if rows, err := db.PendingObservations(ctx, 10); err != nil || len(rows) != 0 {
		t.Fatalf("still pending after upload mark: rows=%+v err=%v", rows, err)
	}
	// The thin key is what a caller holding only the wire batch can build.
	thin := ObsKey{Epoch: key.Epoch, PURL: key.PURL, EnvHash: key.EnvHash,
		Stage: key.Stage, Result: key.Result, ErrorFP: key.ErrorFP}
	if err := db.MarkObservationsPending(ctx, []ObsKey{thin}); err != nil {
		t.Fatal(err)
	}
	after, err := db.PendingObservations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("restore changed the row\nbefore: %+v\nafter:  %+v", before, after)
	}
	// A key that names no row is not an error and creates nothing.
	if err := db.MarkObservationsPending(ctx, []ObsKey{{Epoch: "1999-01-01", PURL: "pkg:npm/nothing@0.0.0"}}); err != nil {
		t.Fatal(err)
	}
	if rows, err := db.PendingObservations(ctx, 10); err != nil || len(rows) != 1 {
		t.Fatalf("an unknown key invented a row: rows=%+v err=%v", rows, err)
	}
}
