package serverstore

import (
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// DependencyEdge is one "this package pulled that version of that library"
// relationship, as the machine holding the lockfile saw it.
//
// VersionCoresidence says two versions of a library were installed together.
// That is the half of the answer nobody can act on; this is the other half —
// which dependency wanted which version, which is the thing a person moves to
// fix the build.
//
// Projects counts distinct project-days, not rebuilds.
type DependencyEdge struct {
	ParentName    string
	ParentVersion string
	ChildName     string
	ChildVersion  string
	Projects      int
}

// edgeClaims turns one batch into the edges it witnesses, as (parent, child)
// purl pairs. The batch's own package is the parent.
func edgeClaims(b domain.ObservationBatch) [][2]domain.PURL {
	parent, err := domain.ParsePURL(b.Package)
	if err != nil || parent.Version == "" || b.Symbol != "" {
		return nil
	}
	out := make([][2]domain.PURL, 0, len(b.DependsOn))
	for _, raw := range b.DependsOn {
		child, err := domain.ParsePURL(raw)
		if err != nil || child.Version == "" {
			continue
		}
		// An ecosystem boundary is not something a lockfile crosses, and an
		// edge that claims to would be a parse error wearing a fact's
		// clothes.
		if child.Ecosystem != parent.Ecosystem {
			continue
		}
		out = append(out, [2]domain.PURL{parent, child})
	}
	return out
}

// sortDependencyEdges groups by the child version being explained, then puts
// the parent most projects have first.
func sortDependencyEdges(out []DependencyEdge) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChildVersion != out[j].ChildVersion {
			return out[i].ChildVersion < out[j].ChildVersion
		}
		if out[i].Projects != out[j].Projects {
			return out[i].Projects > out[j].Projects
		}
		if out[i].ParentName != out[j].ParentName {
			return out[i].ParentName < out[j].ParentName
		}
		return out[i].ParentVersion < out[j].ParentVersion
	})
}

// sortShippedWith groups by this package's own version, newest question
// first: what moved when I upgraded. Within a version the library most
// projects carry leads.
func sortShippedWith(out []DependencyEdge) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].ParentVersion != out[j].ParentVersion {
			return domain.CompareVersions(out[i].ParentVersion, out[j].ParentVersion) > 0
		}
		if out[i].Projects != out[j].Projects {
			return out[i].Projects > out[j].Projects
		}
		if out[i].ChildName != out[j].ChildName {
			return out[i].ChildName < out[j].ChildName
		}
		return out[i].ChildVersion < out[j].ChildVersion
	})
}
