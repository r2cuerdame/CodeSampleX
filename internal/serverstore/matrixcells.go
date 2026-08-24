package serverstore

import "encoding/json"

// MatrixCells is the compatibility grid the site draws, counted.
//
// R2C-89 asks a question the farm panel could not answer: how many of the
// cells a reader actually sees carry an observation. Every stock the panel
// held before this was counted per RELEASE -- a purl either had a passing
// sample or it did not -- and at that grain production reads 99% covered while
// the page a reader opens is mostly dashes. The grain is the whole
// disagreement: a package page spreads SYMBOL against VERSION, so one release
// with forty symbols is forty cells, and thirty-nine of them can be empty
// while the release counts as proven.
//
// So this counts what the grid draws. The cell set is the cross product the
// page renders -- every symbol the network knows for a package against every
// release of it that has a snapshot -- and each cell falls in exactly one of
// three states, which is what the two visually different dashes on the page
// mean:
//
//   - Observed. A real project build reached this coordinate. This is the
//     only state R2C-89 counts as covered.
//   - VerifiedNoObservation. We wrote a sample, it passed here, and no
//     project build has been attributed to the coordinate. The page renders
//     "≡ —": a mark, and a dash where the rate would go. It is a LINK,
//     because the cell holds a document.
//   - Unmeasured. Nothing at all: no evidence row, no sample. The page
//     renders a plain, unlinked "—".
//
// The two dashes read the same to a reader and come from opposite causes, and
// the issue's own reproduction is a page that shows both at once -- which is
// why PackagesShowingBothDashes is counted rather than inferred.
//
// This is an instrument, not a queue. Nothing here hands work out: the
// scheduler's sources are unchanged, and what to DO about each state is the
// open product question recorded on R2C-89. A number that tells an operator
// which of the three is growing is worth having before that is answered, and
// the same census is what will judge the answer afterwards.
type MatrixCells struct {
	// Cells is the denominator: symbol × version cells across every PUBLIC
	// package with a symbol grid. Non-public coordinates are left out for the
	// same reason CoverageHoles leaves them out -- the queue may not offer
	// them, so they are not a backlog anybody can work off.
	Cells int
	// Observed is cells with at least one observation. This is the number
	// R2C-89's completion criterion is written against.
	Observed int
	// VerifiedNoObservation is the linked dash: a sample of ours passed here
	// and no project build has been attributed to it. It is reported apart
	// from Observed and never added to it -- an observation and a
	// verification are different classes of evidence and this repository does
	// not sum them (goal.md §3.5, and the snapshot builder says so in as many
	// words).
	VerifiedNoObservation int
	// Unmeasured is the plain dash: a cell the grid draws because the symbol
	// exists at some other release, with nothing recorded at this one.
	Unmeasured int
	// PackagesShowingBothDashes is how many package pages render a linked and
	// a plain dash at the same time -- the state the issue reproduces from
	// two live URLs. A page in that state cannot be explained to a reader by
	// one sentence about either cause.
	PackagesShowingBothDashes int
}

// matrixCellCounts is the shape this census reads out of a stored snapshot
// document. It is deliberately a local, minimal struct rather than
// compatibility.Snapshot: internal/compatibility imports this package, so
// naming its type here would be an import cycle, and a census that decoded
// the whole document would break on fields it has no opinion about.
type matrixCellCounts struct {
	Rows []struct {
		ObservationClassCounts map[string]int64 `json:"observationClassCounts"`
		VerificationCounts     map[string]int64 `json:"verificationCounts"`
	} `json:"rows"`
}

// snapshotCellEvidence folds one stored snapshot into the two numbers this
// census turns on.
//
// Observations sum every class: the classes split HOW a build was seen, not
// whether it was. Verifications count SAMPLE_VERIFICATION alone --
// "distinctVerifyingPeers" shares the map and is a count of peers, so adding
// it would let one sample verified by three machines read as three runs.
func snapshotCellEvidence(snapshotJSON string) (observations, verifications int64) {
	var doc matrixCellCounts
	if json.Unmarshal([]byte(snapshotJSON), &doc) != nil {
		return 0, 0
	}
	for _, row := range doc.Rows {
		for _, n := range row.ObservationClassCounts {
			observations += n
		}
		verifications += row.VerificationCounts[sampleVerificationClass]
	}
	return observations, verifications
}

// sampleVerificationClass is domain.ClassSampleVerification spelled out. The
// constant lives in internal/domain, which this package already depends on --
// but the string is what is written into the stored document, and the PG half
// of this census matches the same literal in SQL. Written once, so the two
// halves cannot drift.
const sampleVerificationClass = "SAMPLE_VERIFICATION"

// matrixGrid accumulates one package's grid while the census walks cells.
type matrixGrid struct {
	versions              map[string]bool
	symbols               map[string]bool
	measured              int
	observed              int
	verifiedNoObservation int
}

// total is the cross product the page draws: every symbol against every
// release, including the pairs that have no snapshot at all. Those pairs are
// the plain dashes, and they exist only as the difference between this and
// the rows actually stored.
func (g *matrixGrid) total() int { return len(g.versions) * len(g.symbols) }

// foldMatrixGrids reduces per-package grids to the census the panel prints.
// Both stores end here so the Fake and PostgreSQL cannot disagree about the
// arithmetic, only about the rows they feed it.
func foldMatrixGrids(grids map[[2]string]*matrixGrid) MatrixCells {
	var census MatrixCells
	for _, grid := range grids {
		total := grid.total()
		unmeasured := total - grid.measured
		census.Cells += total
		census.Observed += grid.observed
		census.VerifiedNoObservation += grid.verifiedNoObservation
		census.Unmeasured += unmeasured
		if grid.verifiedNoObservation > 0 && unmeasured > 0 {
			census.PackagesShowingBothDashes++
		}
	}
	return census
}
