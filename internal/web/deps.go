package web

import (
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
	seen := map[PackageDep]bool{}
	for _, e := range edges {
		if e.ChildName == "" || e.ChildVersion == "" {
			continue
		}
		seen[PackageDep{
			Library: e.ChildName,
			Version: e.ChildVersion,
			Href:    depHref(ecosystem, e.ChildName, e.ChildVersion),
		}] = true
	}
	out := make([]PackageDep, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Library != out[j].Library {
			return out[i].Library < out[j].Library
		}
		return out[i].Version < out[j].Version
	})
	return out
}
