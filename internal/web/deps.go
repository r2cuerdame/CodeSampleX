package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
)

// depHref points at one library at one exact version — the same pinned view
// this list itself lives on.
func depHref(ecosystem, name, version string) string {
	return pkgHref(ecosystem, name) + "?f_version=" + url.QueryEscape(version)
}

// PackageDep is one first-level dependency of ONE release, at the version
// that release resolved it to.
type PackageDep struct {
	Library string
	Version string
	// Href is that library at that exact version. The row already names the
	// coordinate; making the reader go and find it is a step it was holding
	// the answer to.
	Href string
	// AtlasHref is the same release read from the other side: who else pulled
	// it. A reader deciding whether a version bump is safe wants to know
	// whether anything else in their tree wants a different one.
	AtlasHref string
	// ProjectsText is how many project-days resolved this exact pair. It was
	// measured all along and thrown away at this boundary, and it is the
	// difference between a dependency the whole ecosystem shares and one this
	// release alone pulled.
	Projects     int64
	ProjectsText string
	// State is what this network has MEASURED about the child release itself,
	// not about the edge: "verified" when a contract ran and passed there,
	// "observed" when builds were seen but no contract, "none" when nothing
	// has been measured at that coordinate at all.
	//
	// Never inferred from the edge. A resolver placing two releases side by
	// side says nothing about whether either works, and a row that let a
	// reader read it that way would be the page asserting what it did not
	// measure.
	State     string
	StateText string
}

// buildPackageDeps lists what one pinned release pulled.
//
// This is shown only with a version pinned, and that is the point. Across
// releases the same library appears at several versions, so a page covering
// every release has to choose which to display — a choice nobody asked it to
// make and one a reader cannot check. Pinned, there is exactly one answer.
//
// A library resolved at two versions under a single release is kept as two
// rows rather than collapsed: that is the collision worth seeing, and picking
// one would hide exactly it.
func buildPackageDeps(ecosystem string, edges []DependencyEdge) []PackageDep {
	// Keyed by coordinate rather than by the whole struct: the same pair can
	// arrive on several rows and their project counts have to add up, which a
	// set of structs cannot do.
	type coord struct{ library, version string }
	projects := map[coord]int64{}
	for _, e := range edges {
		if e.ChildName == "" || e.ChildVersion == "" {
			continue
		}
		projects[coord{e.ChildName, e.ChildVersion}] += e.Projects
	}
	out := make([]PackageDep, 0, len(projects))
	for c, n := range projects {
		out = append(out, PackageDep{
			Library:   c.library,
			Version:   c.version,
			Href:      depHref(ecosystem, c.library, c.version),
			AtlasHref: dependencyAtlasHref(ecosystem, c.library, c.version),
			Projects:  n,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Library != out[j].Library {
			return out[i].Library < out[j].Library
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// maxDependencyRows bounds how many dependency rows one release renders, and
// therefore how many snapshot reads a page makes. A first-level dependency
// list is a handful for most releases and a few dozen for a framework; past
// that a reader is not reading rows, they are scrolling past them.
const maxDependencyRows = 40

// dependencyEvidenceState says what this network measured about one child
// release — never about the edge that led to it.
//
//	verified  a contract ran at that coordinate and was recorded
//	observed  builds were seen there, but no contract has run
//	none      nothing has been measured at that coordinate at all
//
// "none" is a gap, not a verdict. It is the state most dependency rows are in,
// and saying so plainly is the point: the alternative is a blank cell a reader
// fills in with an assumption.
func dependencyEvidenceState(r *http.Request, store Store, purl string) string {
	raw, ok := store.SnapshotJSON(r.Context(), purl, "")
	if !ok || raw == "" {
		return "none"
	}
	var doc snapshotDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "none"
	}
	var observed, verified int64
	for _, row := range doc.Rows {
		sp := splitStageCounts(row.ByStage)
		observed += sp.obs + sp.used
		verified += sp.ver
	}
	switch {
	case verified > 0:
		return "verified"
	case observed > 0:
		return "observed"
	default:
		return "none"
	}
}
