package serverstore

// matrixCells counts the compatibility corpus from the Fake's stored snapshots.
// The caller holds f.mu.
//
// It walks the same materialized snapshot documents the site reads, but keeps
// the full corpus cross product instead of applying the site's browse-window
// caps. Counting the corpus from evidence_agg instead would be a second
// definition of what the snapshots contain,
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
		// The release axis takes every release with any snapshot, while the
		// symbol axis takes only real symbols. A release measured only at
		// package grain still contributes its inferred symbol coordinates to
		// the unbounded corpus. The package-level row itself is a total over
		// the symbols beside it, never a cell.
		grid.versions[pkg.Version] = true
		if symbol == "" {
			continue
		}
		grid.symbols[symbol] = true
		grid.measured++
		observations, passingVerifications, failedVerifications := snapshotCellEvidence(snapshotJSON)
		switch {
		case observations > 0:
			grid.observed++
		case passingVerifications > 0 && failedVerifications == 0:
			grid.verifiedNoObservation++
		}
		// A stored row with neither is possible in principle and is left in
		// no bucket but `measured`: a failed verification, for example, is
		// neither an observation, a passing-verification-only coordinate nor
		// an inferred missing coordinate.
	}
	return foldMatrixGrids(grids)
}
