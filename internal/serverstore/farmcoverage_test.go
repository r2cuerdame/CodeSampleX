package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestIntegrationFarmCoverageTimeoutIsDatabaseOwnedAndReusable(t *testing.T) {
	pg := openTestPG(t)
	locked := make(chan struct{})
	release := make(chan struct{})
	lockResult := make(chan error, 1)
	go func() {
		lockResult <- pg.withConn(context.Background(), func(c *pgx.Conn) error {
			tx, err := c.Begin(context.Background())
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err := tx.Exec(context.Background(), `LOCK TABLE packages IN ACCESS EXCLUSIVE MODE`); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("could not establish the blocking coverage fixture")
	}
	seedCtx, seedCancel := context.WithTimeout(context.Background(), time.Second)
	seed, err := pg.pool.acquire(seedCtx)
	seedCancel()
	if err != nil {
		close(release)
		<-lockResult
		t.Fatalf("acquire coverage connection: %v", err)
	}
	coveragePID := seed.conn.PgConn().PID()
	pg.pool.release(seed)

	started := time.Now()
	_, err = pg.farmCoverage(context.Background(), 75*time.Millisecond)
	elapsed := time.Since(started)
	if !IsQueryTimeout(err) {
		close(release)
		<-lockResult
		t.Fatalf("blocked coverage query error = %v, want PostgreSQL statement timeout", err)
	}
	if elapsed >= time.Second {
		close(release)
		<-lockResult
		t.Fatalf("75ms statement timeout returned after %v", elapsed)
	}

	// The lock holder still occupies one pool connection, so this probe must
	// reuse the exact connection whose read-only transaction just timed out.
	// Explicit rollback must clear the aborted transaction without reconnecting.
	reuseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reused, err := pg.pool.acquire(reuseCtx)
	if err != nil {
		close(release)
		<-lockResult
		t.Fatalf("reacquire coverage connection: %v", err)
	}
	var one int
	reuseErr := reused.conn.QueryRow(reuseCtx, `SELECT 1`).Scan(&one)
	reusedPID := reused.conn.PgConn().PID()
	pg.pool.release(reused)
	if reuseErr != nil || one != 1 || reusedPID != coveragePID {
		close(release)
		<-lockResult
		t.Fatalf("coverage connection after timeout: one=%d pid=%d want=%d err=%v",
			one, reusedPID, coveragePID, reuseErr)
	}

	close(release)
	if lockErr := <-lockResult; lockErr != nil {
		t.Fatalf("release coverage fixture: %v", lockErr)
	}
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if _, err := pg.FarmCoverage(ctx); err != nil {
		t.Fatalf("coverage snapshot after timeout: %v", err)
	}
}

// Observations come from developer machines and verifications come from
// containers, so the two can disagree completely about which platform a
// package lives on. Every observation in production is windows and every
// receipt is linux; the panel exists to show that gap rather than let it stay
// invisible.
func TestFarmCoverageSeparatesObservedFromProven(t *testing.T) {
	store := NewFake()
	ctx := context.Background()

	// golang/seen is used on windows and proven on linux.
	// golang/both is used on windows and proven on windows.
	for _, tc := range []struct{ name, observedOS, provenOS string }{
		{"seen", "windows", "linux"},
		{"both", "windows", "windows"},
	} {
		purl := "pkg:golang/example.com/" + tc.name + "@v1.0.0"
		if accepted, _, err := store.IngestBatches(ctx, []domain.ObservationBatch{{
			SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "peer-" + tc.name, ProjectBucket: "proj",
			Package: purl, Symbol: tc.name + ".Call", SymbolConfidence: domain.SymbolProbable,
			Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", OS: tc.observedOS,
				Arch: "amd64", Runtime: "go", RuntimeVersion: "1.26"},
			Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 3,
		}}); err != nil || accepted != 1 {
			t.Fatalf("ingest %s: accepted=%d err=%v", tc.name, accepted, err)
		}
		if err := store.UpsertPackage(ctx, PackageRow{
			PURL: purl, Ecosystem: "golang", Name: "example.com/" + tc.name, Version: "v1.0.0", Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
		id := "sha256:proof-" + tc.name
		if err := store.SaveSample(ctx, SampleRow{SampleID: id,
			ManifestJSON: `{"packages":["` + purl + `"],"symbols":[]}`}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveReceipt(ctx, ReceiptRow{ReceiptID: "r-" + tc.name, SampleID: id,
			ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"` + tc.provenOS + `"}}`}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := store.FarmCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cell := map[string]FarmAxisCoverage{}
	for _, r := range rows {
		cell[r.OS+"/"+r.Ecosystem] = r
	}
	win, ok := cell["windows/golang"]
	if !ok {
		t.Fatalf("no windows/golang cell: %+v", rows)
	}
	if win.Observed != 2 {
		t.Errorf("windows/golang observed = %d, want 2", win.Observed)
	}
	// Only one of the two was actually proven on windows.
	if win.Proven != 1 {
		t.Errorf("windows/golang proven = %d, want 1 — a linux proof is not a windows proof", win.Proven)
	}
}

// resolvedPackages is credited only from a v2 receipt whose resolve stage
// passed; anything else falls back to the manifest. This pins the Fake's
// long-standing rule as the parity reference for PG's coverage query, which
// used to credit any receipt's list — coverage on a package the run never
// resolved.
func TestFarmCoverageCreditsOnlyResolvedV2Lists(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	const declared = "pkg:golang/example.com/declared@v1.0.0"
	const claimed = "pkg:golang/example.com/claimed@v9.9.9"
	for name, purl := range map[string]string{"declared": declared, "claimed": claimed} {
		if err := store.UpsertPackage(ctx, PackageRow{PURL: purl, Ecosystem: "golang",
			Name: "example.com/" + name, Version: "v1.0.0", Publicness: "PUBLIC"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveSample(ctx, SampleRow{SampleID: "sha256:v1credit",
		ManifestJSON: `{"packages":["` + declared + `"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, ReceiptRow{ReceiptID: "r-v1credit", SampleID: "sha256:v1credit",
		ContractResult: "PASS",
		ReceiptJSON:    `{"schemaVersion":1,"environment":{"os":"linux"},"resolvedPackages":["` + claimed + `"]}`}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.FarmCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.OS != "linux" || r.Ecosystem != "golang" {
			continue
		}
		if r.Proven != 1 {
			t.Errorf("linux/golang proven = %d, want only the manifest package credited: %+v", r.Proven, rows)
		}
	}
}
