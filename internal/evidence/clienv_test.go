package evidence

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// gitStatusOutput is one complete CLI execution: the shape evidence.Run
// returns for a wrapped `git status` that succeeded, which leaves the
// termination unset because nothing terminated it.
func gitStatusOutput() CommandOutput {
	started := time.Date(2026, 9, 10, 4, 5, 6, 0, time.UTC)
	return CommandOutput{
		Stdout:      "nothing to commit, working tree clean\n",
		ToolVersion: "2.51.0",
		Shell:       "direct",
		StartedAt:   started,
		FinishedAt:  started.Add(400 * time.Millisecond),
	}
}

// recordedEnvironment returns the fingerprint the only pending observation
// was filed under, read back the way the uploader reads it.
func recordedEnvironment(t *testing.T, db *localdb.DB) domain.EnvironmentFingerprint {
	t.Helper()
	rows := pendingRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("pending observations = %d, want 1", len(rows))
	}
	env, found, err := db.GetEnvironment(context.Background(), rows[0].EnvHash)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if !found {
		t.Fatalf("no environment stored for hash %q", rows[0].EnvHash)
	}
	return env
}

func communityRecorder(t *testing.T, db *localdb.DB) *Recorder {
	t.Helper()
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	return &Recorder{DB: db, Ident: testIdentity(t), Cfg: cfg}
}

// A CLI tool run outside any scanned workspace has no scan result at all.
// The fingerprint must still name the host, because an environment without
// ecosystem, os and arch makes the whole batch unuploadable.
func TestRecordCommandOutputFingerprintsHostWhenScanIsAbsent(t *testing.T) {
	db := testDB(t)
	rec := communityRecorder(t, db)

	if err := rec.RecordCommandOutput(context.Background(), t.TempDir(), nil,
		scanner.CommandProfile{}, []string{"git", "status", "--short"}, 0, gitStatusOutput()); err != nil {
		t.Fatalf("RecordCommandOutput: %v", err)
	}

	env := recordedEnvironment(t, db)
	if env.Ecosystem == "" || env.OS == "" || env.Arch == "" {
		t.Fatalf("environment missing required axes: ecosystem=%q os=%q arch=%q",
			env.Ecosystem, env.OS, env.Arch)
	}
}

// The reachable production case: the directory was scanned, but no adapter
// detected an ecosystem there, so scanner.Scan leaves Ecosystem empty while
// os and arch are already correct. Only the empty axis may be filled.
func TestRecordCommandOutputKeepsScannedAxesAndFillsOnlyTheEmptyEcosystem(t *testing.T) {
	db := testDB(t)
	rec := communityRecorder(t, db)

	scanned := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "arm64"}
	if err := rec.RecordCommandOutput(context.Background(), t.TempDir(), &scanner.ScanResult{Env: scanned},
		scanner.CommandProfile{}, []string{"git", "status", "--short"}, 0, gitStatusOutput()); err != nil {
		t.Fatalf("RecordCommandOutput: %v", err)
	}

	env := recordedEnvironment(t, db)
	if env.OS != "linux" || env.Arch != "arm64" {
		t.Errorf("scanned axes were overwritten: os=%q arch=%q, want linux/arm64", env.OS, env.Arch)
	}
	if env.Ecosystem == "" {
		t.Error("empty ecosystem was left empty")
	}
}

// A scan that did name an ecosystem is authoritative; nothing is invented
// over it, and the observation keeps the fingerprint the scan produced.
func TestRecordCommandOutputLeavesACompleteScanEnvironmentUntouched(t *testing.T) {
	db := testDB(t)
	rec := communityRecorder(t, db)

	scanned := testEnvFP()
	if err := rec.RecordCommandOutput(context.Background(), t.TempDir(), &scanner.ScanResult{Env: scanned},
		scanner.CommandProfile{}, []string{"git", "status", "--short"}, 0, gitStatusOutput()); err != nil {
		t.Fatalf("RecordCommandOutput: %v", err)
	}

	if got := recordedEnvironment(t, db).Hash(); got != scanned.Hash() {
		t.Fatalf("environment hash = %q, want the scanned fingerprint %q", got, scanned.Hash())
	}
}

// The acceptance the daemon log reported: the environment on a batch built
// from a CLI run outside a workspace is one the real ingest validator takes.
func TestBatchFromCLIRunOutsideWorkspaceCarriesAnAcceptableEnvironment(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}

	if err := rec.RecordCommandOutput(context.Background(), t.TempDir(), nil,
		scanner.CommandProfile{}, []string{"git", "status", "--short"}, 0, gitStatusOutput()); err != nil {
		t.Fatalf("RecordCommandOutput: %v", err)
	}

	batches, _, err := (&Batcher{DB: db, Ident: ident, Cfg: cfg}).build(context.Background())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}

	b := batches[0]
	if b.ProjectBucket == "" {
		// A separate, independently reported defect on the same path: a CLI
		// observation files no symbol sighting, so the client derives no
		// project bucket for its generic coordinate and the server refuses
		// the batch on that field first. Supplying one here keeps this test
		// measuring the environment axis and nothing else; remove this once
		// the bucket is fixed, and the assertion below covers both.
		b.ProjectBucket = "bucket-standing-in-for-the-missing-one"
	}
	if err := serverstore.ValidateBatch(b); err != nil {
		t.Fatalf("ValidateBatch: %v", err)
	}
}
