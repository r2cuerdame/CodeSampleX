package daemon

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Checking the queue is the privacy-verification step the product asks the
// user to take (goal.md §12.5), and it used to destroy what it showed: the
// preview drained the batcher, then "restored" the rows with a partial key
// whose UPSERT overwrote every failure-evidence and dependency column with
// its zero value (#338). Reading the queue must leave the store untouched.
func TestQueuePreviewLeavesPendingRowsByteForByteIdentical(t *testing.T) {
	home := newTestHome(t, nil)
	d, c := startDaemon(t, home)
	ctx := context.Background()

	env := testEnv()
	if err := d.DB.SaveEnvironment(ctx, env); err != nil {
		t.Fatal(err)
	}
	exit := 1
	rich := localdb.ObsKey{
		Epoch: "2026-09-18", PURL: "pkg:npm/axios@1.12.0", Symbol: "",
		EnvHash: env.Hash(), Stage: domain.StageProjectCompile, Result: domain.ResultFail,
		ErrorFP: "fp-rich", ErrorCode: "TS2307",
		TerminationKind: domain.TerminationExit, ExitCode: &exit, Signal: "",
		TimeoutMillis: 0, ErrorSummary: "syntax error", EvidenceQuality: domain.EvidenceComplete,
		OuterCommand: "npm run build", OuterStage: domain.StageProjectCompile,
		ActualToolchain: "tsc", StageEvidence: domain.FailureStageCompilerDiagnostic,
		FailureEvidenceGap: "",
		Direct:             true,
		Coresident:         []string{"pkg:npm/axios@0.27.2"},
		DependsOn:          []string{"pkg:npm/follow-redirects@1.15.6"},
	}
	if err := d.DB.RecordObservation(ctx, rich, 3); err != nil {
		t.Fatal(err)
	}
	// A leaf package resolved with nothing under it: the one flag the
	// dependency axis cannot infer (#361) and the easiest one to lose.
	leaf := localdb.ObsKey{
		Epoch: "2026-09-18", PURL: "pkg:npm/leftpad@1.0.0",
		EnvHash: env.Hash(), Stage: domain.StageProjectCompile, Result: domain.ResultPass,
		Direct: true, DependsOnNone: true,
	}
	if err := d.DB.RecordObservation(ctx, leaf, 1); err != nil {
		t.Fatal(err)
	}
	// A row written by a pre-fix Windows client carries the unsigned DWORD
	// spelling of its exit code. The batcher canonicalizes it on the wire,
	// so the batch fingerprint differs from the stored key; a restore keyed
	// on the batch used to leave this row marked uploaded forever and add an
	// orphan row with count 0 under the canonical fingerprint.
	legacyExit := int(uint32(0xC0000005))
	legacyTerm := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &legacyExit}
	legacy := localdb.ObsKey{
		Epoch: "2026-09-18", PURL: "pkg:npm/esbuild@0.23.0",
		EnvHash: env.Hash(), Stage: domain.StageProjectCompile, Result: domain.ResultFail,
		ErrorFP:         domain.FailureFingerprint(domain.StageProjectCompile, legacyTerm, "E_ACCESS", "access violation"),
		ErrorCode:       "E_ACCESS",
		TerminationKind: domain.TerminationExit, ExitCode: &legacyExit,
		ErrorSummary: "access violation", EvidenceQuality: domain.EvidenceComplete,
	}
	if err := d.DB.RecordObservation(ctx, legacy, 2); err != nil {
		t.Fatal(err)
	}

	before, err := d.DB.PendingObservations(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 {
		t.Fatalf("seeded 3 pending rows, store holds %d", len(before))
	}

	for i := 0; i < 2; i++ {
		res, err := http.Get(d.BaseURL() + "/local/v1/queue")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /local/v1/queue: %d", res.StatusCode)
		}
		preview, err := c.Queue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Batches) != 3 {
			t.Fatalf("preview %d: %d batches, want 3", i, len(preview.Batches))
		}
	}

	after, err := d.DB.PendingObservations(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the privacy preview changed the pending rows\nbefore: %+v\nafter:  %+v", before, after)
	}
	// The preview must not have invented a canonical-fingerprint sibling
	// for the legacy Windows row: the store still holds exactly the rows
	// that were seeded, and every one of them is still pending.
	for _, key := range []localdb.ObsKey{rich, leaf, legacy} {
		n, found, err := d.DB.ObservationCount(ctx, key)
		if err != nil || !found || n == 0 {
			t.Fatalf("row %s lost by the preview: count=%d found=%v err=%v", key.PURL, n, found, err)
		}
	}
}
