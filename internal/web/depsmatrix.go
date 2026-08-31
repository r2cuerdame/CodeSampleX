package web

import (
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// maxMatrixRows bounds the comparison the same way the dependency table is
// bounded. A package can have hundreds of children and the movers are what
// the reader came for, so the cap falls on the steady ones.
const maxMatrixRows = 40

// dependencyMatrixCell is what one release resolved one child to.
//
// An empty Version is not "this release does not depend on it". It is an
// absent measurement: no resolution recorded that pair, and a resolver that
// never ran says nothing about what it would have found.
type dependencyMatrixCell struct {
	Version string
}

type dependencyMatrixRow struct {
	Child string
	Cells []dependencyMatrixCell
	// Moves is true when this child resolved to more than one version across
	// the releases below. It is the only thing on the page that points at a
	// boundary, so it leads the table.
	Moves bool
}

// dependencyMatrix is what each release of one package resolved its children
// to, side by side.
//
// The table above it answers "what shipped with THIS release". The question a
// reader arrives with when a build breaks after an upgrade is the other one:
// which child moved between the release that worked and the one that does not.
// Measured on production, 453 (parent, child) pairs across 174 packages
// resolve differently at different releases of the parent, and nothing on the
// site could show one of them.
type dependencyMatrix struct {
	// Versions are the parent's releases, newest first.
	Versions []string
	Rows     []dependencyMatrixRow
	// Moved is how many children resolved to more than one version, and
	// Steady how many did not. Both are stated because a table of movers
	// alone would read as though the whole tree changed.
	Moved  int
	Steady int
	// Truncated says rows were dropped at the cap. Steady still counts them:
	// an absent row must never read as an absent dependency.
	Truncated bool
}

// buildDependencyMatrix folds every edge of one package into the comparison.
//
// It takes the rows the dependency table already fetched rather than asking
// the store again: Dependencies returns every release's edges and the table
// throws away all but the pinned one.
//
// A single release returns nil. Every row would repeat the table above it, and
// a one-column grid dressed as a comparison invites a reader to see a trend in
// one point.
func buildDependencyMatrix(edges []DependencyEdge) *dependencyMatrix {
	byChild := map[string]map[string]string{}
	versions := map[string]bool{}
	for _, e := range edges {
		if e.ParentVersion == "" || e.ChildName == "" {
			continue
		}
		versions[e.ParentVersion] = true
		if byChild[e.ChildName] == nil {
			byChild[e.ChildName] = map[string]string{}
		}
		byChild[e.ChildName][e.ParentVersion] = e.ChildVersion
	}
	if len(versions) < 2 {
		return nil
	}
	order := make([]string, 0, len(versions))
	for v := range versions {
		order = append(order, v)
	}
	sort.Slice(order, func(i, j int) bool {
		return domain.CompareVersions(order[i], order[j]) > 0
	})

	m := &dependencyMatrix{Versions: order}
	rows := make([]dependencyMatrixRow, 0, len(byChild))
	for child, at := range byChild {
		row := dependencyMatrixRow{Child: child, Cells: make([]dependencyMatrixCell, len(order))}
		seen := map[string]bool{}
		for i, v := range order {
			cv := at[v]
			row.Cells[i] = dependencyMatrixCell{Version: cv}
			if cv != "" {
				seen[cv] = true
			}
		}
		row.Moves = len(seen) > 1
		if row.Moves {
			m.Moved++
		} else {
			m.Steady++
		}
		rows = append(rows, row)
	}
	// Movers first: they are the only rows that answer the question. Within
	// each group, by name, so the order does not shuffle between releases.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Moves != rows[j].Moves {
			return rows[i].Moves
		}
		return rows[i].Child < rows[j].Child
	})
	if len(rows) > maxMatrixRows {
		rows = rows[:maxMatrixRows]
		m.Truncated = true
	}
	m.Rows = rows
	return m
}
