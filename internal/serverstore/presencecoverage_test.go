package serverstore

import (
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func presenceObs(f *Fake, t *testing.T, purl, stage string, n int) {
	t.Helper()
	acc, rej, err := f.IngestBatches(t.Context(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "anon-1", ProjectBucket: "proj-1",
		Package: purl, Stage: domain.Stage(stage), Result: domain.ResultPass,
		ObservationCount: n,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
			Runtime: "node", RuntimeVersion: "22",
		},
	}})
	if err != nil || acc != 1 {
		t.Fatalf("ingest %s %s: acc=%d rej=%+v err=%v", purl, stage, acc, rej, err)
	}
}

// The instrument's thinnest place is not a platform it cannot reach: it is
// the packages it has only ever seen INSTALLED. 1,167 npm versions in
// production have presence records and have never once been exercised, and a
// coverage disclosure that does not say so lets a reader take "2,731 packages
// with evidence" as 2,731 packages that were run.
func TestPresenceOnlyCoverageNamesWhatWasNeverExercised(t *testing.T) {
	f := NewFake()
	presenceObs(f, t, "pkg:npm/never-run@1.0.0", "USED", 40)
	presenceObs(f, t, "pkg:npm/actually-built@2.0.0", "USED", 5)
	presenceObs(f, t, "pkg:npm/actually-built@2.0.0", "PROJECT_TEST", 12)

	rows, err := f.PresenceOnlyCoverage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one ecosystem", rows)
	}
	got := rows[0]
	if got.Ecosystem != "npm" {
		t.Errorf("ecosystem = %q, want npm", got.Ecosystem)
	}
	if got.PresenceOnly != 1 {
		t.Errorf("presence only = %d, want 1", got.PresenceOnly)
	}
	if got.Exercised != 1 {
		t.Errorf("exercised = %d, want 1", got.Exercised)
	}
}

var _ = time.Now
