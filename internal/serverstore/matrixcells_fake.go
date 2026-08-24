package serverstore

// matrixCells counts the compatibility grid from the Fake's stored snapshots.
// The caller holds f.mu.
//
// It walks the same rows the site reads -- the materialized snapshot
// documents, not the evidence they were folded from. Counting the grid from
// evidence_agg instead would be a second definition of what the page shows,
// and the two halves of this panel have drifted apart before for exactly that
// reason: a stock counted from a different predicate than the thing it
// describes moves when the thing does not.
func (f *Fake) matrixCells() MatrixCells {
	grids := map[[2]string]*matrixGrid{}
	for key, snapshotJSON := range f.snapshots {
		purl, symbol := key[0], key[1]
		pkg, ok := f.packages[purl]
		if !ok || pkg.Version == "" || pkg.Publicness != "PUBLIC" {
			continue
		}
		name := [2]string{pkg.Ecosystem, pkg.Name}
		grid := grids[name]
		if grid == nil {
			grid = &matrixGrid{versions: map[string]bool{}, symbols: map[string]bool{}}
			grids[name] = grid
		}
		// The release axis takes every release with any snapshot, the symbol
		// axis only real symbols. A release measured at package grain and
		// never at symbol grain still gets its column on the page, and every
		// symbol row draws a plain dash in it -- so leaving it out would drop
		// exactly the columns this census exists to count. The package-level
		// row itself is a total over the symbols beside it, never a cell.
		grid.versions[pkg.Version] = true
		if symbol == "" {
			continue
		}
		grid.symbols[symbol] = true
		grid.measured++
		observations, verifications := snapshotCellEvidence(snapshotJSON)
		switch {
		case observations > 0:
			grid.observed++
		case verifications > 0:
			grid.verifiedNoObservation++
		}
		// A stored row with neither is possible in principle and is left in
		// no bucket but `measured`: it is not a dash a reader can act on and
		// inventing a fourth state for it would be a number with no page
		// behind it.
	}
	return foldMatrixGrids(grids)
}
