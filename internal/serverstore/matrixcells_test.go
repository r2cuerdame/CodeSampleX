package serverstore

import (
	"context"
	"testing"
	"time"
)

// matrixCellStore is the slice of a store the grid census needs: a package
// row to hang the coordinate on and the materialized snapshot the page reads.
type matrixCellStore interface {
	UpsertPackage(context.Context, PackageRow) error
	PutSnapshot(ctx context.Context, purl, symbol, snapshotJSON string) error
	FarmBacklogNow(context.Context, time.Time, time.Time) (FarmBacklog, error)
}

// gridCell is one (release, symbol) coordinate as production holds it: a
// stored snapshot with some number of observations and some number of our own
// verified runs. symbol "" is the package-level row.
type gridCell struct {
	ecosystem, name, version, symbol string
	observations, verifications      int
	failedVerifications              int
	// publicness defaults to PUBLIC; a case that needs the exclusion says so.
	publicness string
}

func (c gridCell) purl() string {
	return "pkg:" + c.ecosystem + "/" + c.name + "@" + c.version
}

// snapshotJSON writes the document shape the aggregation pipeline stores.
// CONTRACT pass/fail decides the rendered verification state; the aggregate
// verification count intentionally includes both and must not classify it.
func (c gridCell) snapshotJSON() string {
	doc := `{"schemaVersion":1,"purl":"` + c.purl() + `","symbol":"` + c.symbol + `","rows":[{` +
		`"contextLabel":"node 22","observationClassCounts":{`
	if c.observations > 0 {
		doc += `"USAGE_OBSERVATION":` + itoa(c.observations)
	}
	doc += `},"byStage":{`
	if c.verifications > 0 || c.failedVerifications > 0 {
		doc += `"CONTRACT":{"pass":` + itoa(c.verifications) + `,"fail":` + itoa(c.failedVerifications) + `}`
	}
	doc += `},"verificationCounts":{`
	if total := c.verifications + c.failedVerifications; total > 0 {
		doc += `"SAMPLE_VERIFICATION":` + itoa(total) + `,"distinctVerifyingPeers":1`
	}
	doc += `}}]}`
	return doc
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func seedGrid(t *testing.T, store matrixCellStore, cells []gridCell) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC)
	for _, cell := range cells {
		publicness := cell.publicness
		if publicness == "" {
			publicness = "PUBLIC"
		}
		if err := store.UpsertPackage(ctx, PackageRow{
			PURL: cell.purl(), Ecosystem: cell.ecosystem, Name: cell.name,
			Version: cell.version, Publicness: publicness,
			FirstSeen: now, LastSeen: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutSnapshot(ctx, cell.purl(), cell.symbol, cell.snapshotJSON()); err != nil {
			t.Fatal(err)
		}
	}
}

// productionGrid is the two pages R2C-89 reproduces from, copied cell for cell
// off the live site on 2026-08-24.
//
// Both of them render a LINKED dash and a PLAIN dash at the same time, which
// is the state the issue calls unfinished, and the two dashes come from
// opposite causes:
//
//	github.com/jackc/pgx/v5   v5.10.0   v5.9.2   v5.7.3
//	(package total)           ≡ 82% 1288  ≡ 100% 4  ≡ 100% 2
//	AppendRows                ≡ —         —         —
//	Batch                     ≡ 81% 748   —         —
//
// AppendRows@v5.10.0 is linked: we wrote a sample, it passed, nobody has been
// seen using it. The v5.9.2 column is plain all the way down: the release has
// a package-level snapshot and no symbol was ever measured at it -- which is
// also why the column exists at all.
//
// The npm/semver page shows the same two states in a second ecosystem, which
// is the point of carrying it: a fix that closed pgx and left semver alone
// would pass a one-package fixture.
func productionGrid() []gridCell {
	return []gridCell{
		// --- golang/github.com/jackc/pgx/v5 -------------------------------
		{ecosystem: "golang", name: "github.com/jackc/pgx/v5", version: "v5.10.0", symbol: "", observations: 1288, verifications: 1},
		{ecosystem: "golang", name: "github.com/jackc/pgx/v5", version: "v5.10.0", symbol: "AppendRows", verifications: 1},
		{ecosystem: "golang", name: "github.com/jackc/pgx/v5", version: "v5.10.0", symbol: "Batch", observations: 748, verifications: 1},
		// Two releases measured only at package grain: their columns are
		// drawn and every symbol row in them is a plain dash.
		{ecosystem: "golang", name: "github.com/jackc/pgx/v5", version: "v5.9.2", symbol: "", observations: 4, verifications: 1},
		{ecosystem: "golang", name: "github.com/jackc/pgx/v5", version: "v5.7.3", symbol: "", observations: 2, verifications: 1},

		// --- npm/semver ---------------------------------------------------
		{ecosystem: "npm", name: "semver", version: "7.7.1", symbol: "", observations: 4, verifications: 1},
		{ecosystem: "npm", name: "semver", version: "7.7.1", symbol: "semver.clean", verifications: 1},
		{ecosystem: "npm", name: "semver", version: "7.7.1", symbol: "semver.diff", verifications: 1},
		{ecosystem: "npm", name: "semver", version: "6.3.1", symbol: "", observations: 122, verifications: 1},
		{ecosystem: "npm", name: "semver", version: "6.3.1", symbol: "semver.clean", observations: 4},
		{ecosystem: "npm", name: "semver", version: "6.3.1", symbol: "semver.diff", observations: 4},
		{ecosystem: "npm", name: "semver", version: "7.8.0", symbol: "", observations: 2, verifications: 1},
	}
}

// The census has to see BOTH dashes on both pages. A count that pooled them
// into one "empty" figure would report the same number for a page we have
// already run and a page nothing has ever touched, and those are answered by
// different work -- which is the distinction the issue turns on.
func TestMatrixCensusSeesBothDashFormsOnOnePage(t *testing.T) {
	fake := NewFake()
	seedGrid(t, fake, productionGrid())

	backlog, err := fake.FarmBacklogNow(context.Background(),
		time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got := backlog.Matrix

	// pgx: 3 releases × 2 symbols = 6 cells. Measured: AppendRows@v5.10.0
	// (linked) and Batch@v5.10.0 (observed). The other four are plain.
	// semver: 3 releases × 2 symbols = 6 cells. Measured: clean and diff at
	// 7.7.1 (linked) and at 6.3.1 (observed). 7.8.0's two are plain.
	want := MatrixCells{
		Cells:                     12,
		Observed:                  3,
		VerifiedNoObservation:     3,
		Unmeasured:                6,
		PackagesShowingBothDashes: 2,
	}
	if got != want {
		t.Errorf("matrix census = %+v, want %+v", got, want)
	}
}

// A release that carries only its package-level snapshot still has a column
// on the page, and every symbol row draws a plain dash in it. Counting the
// release axis from symbol rows alone would drop exactly the columns that are
// entirely dashes -- the census would report a full grid for a page a reader
// sees as mostly empty.
func TestMatrixCensusCountsReleasesMeasuredOnlyAtPackageGrain(t *testing.T) {
	fake := NewFake()
	seedGrid(t, fake, []gridCell{
		{ecosystem: "npm", name: "left-pad", version: "1.3.0", symbol: "", observations: 9},
		{ecosystem: "npm", name: "left-pad", version: "1.3.0", symbol: "leftPad", observations: 9},
		{ecosystem: "npm", name: "left-pad", version: "1.2.0", symbol: "", observations: 1},
	})
	census := fake.matrixCells()
	if census.Cells != 2 {
		t.Errorf("cells = %d, want 2 (two releases × one symbol)", census.Cells)
	}
	if census.Unmeasured != 1 {
		t.Errorf("unmeasured = %d, want 1 (leftPad@1.2.0 is a plain dash)", census.Unmeasured)
	}
	if census.Observed != 1 {
		t.Errorf("observed = %d, want 1", census.Observed)
	}
}

// The package-level row is a total over the symbols beside it. Counting it as
// a cell would add a row to its own sum -- the same reason assembleGrid
// leaves the aggregate out of the tallies it prints.
func TestMatrixCensusExcludesThePackageLevelTotal(t *testing.T) {
	fake := NewFake()
	seedGrid(t, fake, []gridCell{
		{ecosystem: "npm", name: "left-pad", version: "1.3.0", symbol: "", observations: 9},
		{ecosystem: "npm", name: "left-pad", version: "1.3.0", symbol: "leftPad", observations: 9},
	})
	census := fake.matrixCells()
	if census.Cells != 1 || census.Observed != 1 {
		t.Errorf("census = %+v, want one cell, one observed", census)
	}
}

// Verifications are counted apart from observations and never added to them.
// In particular, a verification must not satisfy a criterion written about
// observations.
func TestMatrixCensusNeverSumsVerificationsIntoObservations(t *testing.T) {
	fake := NewFake()
	seedGrid(t, fake, []gridCell{
		{ecosystem: "npm", name: "solo", version: "1.0.0", symbol: "", verifications: 3},
		{ecosystem: "npm", name: "solo", version: "1.0.0", symbol: "run", verifications: 3},
	})
	census := fake.matrixCells()
	if census.Observed != 0 {
		t.Errorf("observed = %d, want 0: three verified runs are not an observation", census.Observed)
	}
	if census.VerifiedNoObservation != 1 {
		t.Errorf("verifiedNoObservation = %d, want 1", census.VerifiedNoObservation)
	}
}

// verificationCounts includes both PASS and FAIL receipts. A failed contract
// is rendered as a cross, not the linked dash that says our sample passed, so
// it must not enter VerifiedNoObservation or make a package look like it has
// both dash states.
func TestMatrixCensusClassifiesOnlyPassingContractsAsVerifiedOnly(t *testing.T) {
	fake := NewFake()
	seedGrid(t, fake, []gridCell{
		{ecosystem: "npm", name: "broken", version: "2.0.0", symbol: "run", failedVerifications: 2},
		{ecosystem: "npm", name: "broken", version: "1.0.0", symbol: ""},
		{ecosystem: "npm", name: "mixed", version: "2.0.0", symbol: "run", verifications: 1, failedVerifications: 1},
		{ecosystem: "npm", name: "mixed", version: "1.0.0", symbol: ""},
	})
	census := fake.matrixCells()
	if census.Cells != 4 || census.Unmeasured != 2 {
		t.Errorf("census = %+v, want four corpus cells with two unmeasured", census)
	}
	if census.VerifiedNoObservation != 0 {
		t.Errorf("verifiedNoObservation = %d, want 0 for failed contracts", census.VerifiedNoObservation)
	}
	if census.PackagesShowingBothDashes != 0 {
		t.Errorf("packagesShowingBothDashes = %d, want 0 without a passing contract", census.PackagesShowingBothDashes)
	}
}

// MatrixCells is the completeness denominator, not the package page's
// bounded browse window. Keep older versions and late symbols in the corpus
// even when they exceed the UI's current six-version, ten-symbol and six-row
// display caps.
func TestMatrixCensusIsUnboundedCorpusCrossProduct(t *testing.T) {
	var cells []gridCell
	for version := 1; version <= 7; version++ {
		v := itoa(version) + ".0.0"
		cells = append(cells, gridCell{ecosystem: "npm", name: "wide", version: v, symbol: ""})
		if version == 1 {
			for symbol := 1; symbol <= 11; symbol++ {
				cells = append(cells, gridCell{
					ecosystem: "npm", name: "wide", version: v,
					symbol: "symbol" + itoa(symbol), observations: 1,
				})
			}
		}
	}
	fake := NewFake()
	seedGrid(t, fake, cells)
	census := fake.matrixCells()
	if census.Cells != 77 || census.Observed != 11 || census.Unmeasured != 66 {
		t.Errorf("census = %+v, want all 7 versions x 11 symbols in the corpus", census)
	}
}

// Non-public coordinates are left out of the denominator for the same reason
// CoverageHoles leaves them out: the queue may not offer them, so they are
// not a backlog anybody can work off, and counting them would make the
// completion figure unreachable by design.
func TestMatrixCensusCountsOnlyPublicCoordinates(t *testing.T) {
	fake := NewFake()
	seedGrid(t, fake, []gridCell{
		{ecosystem: "npm", name: "inner", version: "1.0.0", symbol: "", publicness: "PRIVATE"},
		{ecosystem: "npm", name: "inner", version: "1.0.0", symbol: "hidden", publicness: "PRIVATE"},
		{ecosystem: "npm", name: "unsure", version: "1.0.0", symbol: "", publicness: "UNKNOWN"},
		{ecosystem: "npm", name: "unsure", version: "1.0.0", symbol: "maybe", publicness: "UNKNOWN"},
	})
	if census := fake.matrixCells(); census != (MatrixCells{}) {
		t.Errorf("census = %+v, want empty: neither coordinate is public", census)
	}
}

// The census lives in two implementations -- a Go walk over the Fake's
// snapshot map and a jsonb aggregate over compatibility_snapshots -- and this
// repository's own comments record what happens when two halves of one
// definition drift: a panel reports a figure the server would never serve. So
// the production grid is replayed into both stores and the five numbers are
// compared.
func TestIntegrationMatrixCensusFakeMatchesPostgres(t *testing.T) {
	cells := productionGrid()
	// A private coordinate and a release with no symbol row at all, on top of
	// the two live pages: the exclusions are where a Go filter and a SQL
	// predicate are most likely to disagree.
	cells = append(cells,
		gridCell{ecosystem: "npm", name: "inner", version: "1.0.0", symbol: "", publicness: "PRIVATE"},
		gridCell{ecosystem: "npm", name: "inner", version: "1.0.0", symbol: "hidden", publicness: "PRIVATE", verifications: 1},
		gridCell{ecosystem: "npm", name: "quiet", version: "2.0.0", symbol: "", observations: 3},
		// Aggregate verificationCounts is positive, but CONTRACT has only
		// failures. Both stores must leave this out of verified-only.
		gridCell{ecosystem: "npm", name: "broken", version: "2.0.0", symbol: "run", failedVerifications: 2},
		gridCell{ecosystem: "npm", name: "broken", version: "1.0.0", symbol: ""},
		gridCell{ecosystem: "npm", name: "mixed", version: "2.0.0", symbol: "run", verifications: 1, failedVerifications: 1},
		gridCell{ecosystem: "npm", name: "mixed", version: "1.0.0", symbol: ""},
	)

	ctx := context.Background()
	since := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	until := time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC)

	fake := NewFake()
	seedGrid(t, fake, cells)
	pg := openTestPG(t)
	seedGrid(t, pg, cells)

	fakeBacklog, err := fake.FarmBacklogNow(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	pgBacklog, err := pg.FarmBacklogNow(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if fakeBacklog.Matrix != pgBacklog.Matrix {
		t.Errorf("matrix census: fake=%+v pg=%+v", fakeBacklog.Matrix, pgBacklog.Matrix)
	}
	// Both halves must also be RIGHT, not merely equal: two implementations
	// of the same mistake agree perfectly.
	want := MatrixCells{
		Cells:                     16,
		Observed:                  3,
		VerifiedNoObservation:     3,
		Unmeasured:                8,
		PackagesShowingBothDashes: 2,
	}
	if fakeBacklog.Matrix != want {
		t.Errorf("matrix census = %+v, want %+v", fakeBacklog.Matrix, want)
	}
}
