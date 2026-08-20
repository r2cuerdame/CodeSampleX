package web

import (
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ShipsWithGrid is "what came with each release of this package": rows are
// the libraries it pulled, columns are its own versions.
//
// Upgrade a library and its dependencies move under you, and the one that
// moved is usually the one that broke the build. A lockfile records exactly
// this and nothing else could: the server receives one package per record.
type ShipsWithGrid struct {
	// Versions of THIS package, newest first — the reader is asking what an
	// upgrade moved.
	Versions []string
	Rows     []ShipsWithRow
}

// ShipsWithRow is one referenced library across those versions.
type ShipsWithRow struct {
	Library string
	// Cells is aligned with Versions by index. Empty means this release never
	// shipped that library, which is a different fact from shipping an
	// unknown version of it.
	Cells []string
}

// shipsWithCap bounds the grid. A package with a long release history and a
// wide dependency set would otherwise render a wall; the explorer is where
// completeness lives.
const shipsWithCap = 8

// buildShipsWith turns dependency edges into the library × version grid.
//
// A cell holds EVERY version that shipped there. Two versions of one library
// under a single release is the collision worth seeing, and picking one would
// hide exactly that.
func buildShipsWith(edges []DependencyEdge) ShipsWithGrid {
	byVersion := map[string]map[string]map[string]bool{}
	weight := map[string]int{}
	for _, e := range edges {
		if e.ParentVersion == "" || e.ChildName == "" || e.ChildVersion == "" {
			continue
		}
		if byVersion[e.ParentVersion] == nil {
			byVersion[e.ParentVersion] = map[string]map[string]bool{}
		}
		if byVersion[e.ParentVersion][e.ChildName] == nil {
			byVersion[e.ParentVersion][e.ChildName] = map[string]bool{}
		}
		byVersion[e.ParentVersion][e.ChildName][e.ChildVersion] = true
		weight[e.ChildName] += int(e.Projects)
	}
	versions := make([]string, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.SliceStable(versions, func(i, j int) bool {
		if c := domain.CompareVersions(versions[i], versions[j]); c != 0 {
			return c > 0
		}
		return versions[i] < versions[j]
	})
	if len(versions) > shipsWithCap {
		versions = versions[:shipsWithCap]
	}

	libs := make([]string, 0, len(weight))
	for lib := range weight {
		libs = append(libs, lib)
	}
	sort.SliceStable(libs, func(i, j int) bool {
		if weight[libs[i]] != weight[libs[j]] {
			return weight[libs[i]] > weight[libs[j]]
		}
		return libs[i] < libs[j]
	})
	if len(libs) > shipsWithCap {
		libs = libs[:shipsWithCap]
	}

	grid := ShipsWithGrid{Versions: versions}
	for _, lib := range libs {
		row := ShipsWithRow{Library: lib, Cells: make([]string, len(versions))}
		for i, v := range versions {
			seen := byVersion[v][lib]
			if len(seen) == 0 {
				continue
			}
			list := make([]string, 0, len(seen))
			for cv := range seen {
				list = append(list, cv)
			}
			sort.Strings(list)
			row.Cells[i] = strings.Join(list, " · ")
		}
		grid.Rows = append(grid.Rows, row)
	}
	return grid
}

// filterEdgesToVersion keeps the edges of ONE release of this package.
//
// Pinning a version filters the page, and half the page was ignoring it:
// ?f_version=2.0.4 narrowed the cube and left these tables showing every
// release. A page that says it is filtered has to be.
func filterEdgesToVersion(edges []DependencyEdge, version string) []DependencyEdge {
	if version == "" {
		return edges
	}
	out := make([]DependencyEdge, 0, len(edges))
	for _, e := range edges {
		if e.ParentVersion == version {
			out = append(out, e)
		}
	}
	return out
}

// filterDependantsToVersion keeps what pulled ONE version of this package.
// The table already answers "who pulled this version", so pinning one is the
// question it was shaped for.
func filterDependantsToVersion(edges []DependencyEdge, version string) []DependencyEdge {
	if version == "" {
		return edges
	}
	out := make([]DependencyEdge, 0, len(edges))
	for _, e := range edges {
		if e.ChildVersion == version {
			out = append(out, e)
		}
	}
	return out
}

// Empty reports whether there is nothing to draw.
func (g ShipsWithGrid) Empty() bool { return len(g.Rows) == 0 || len(g.Versions) == 0 }
