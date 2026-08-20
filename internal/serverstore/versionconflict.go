package serverstore

import "sort"

// VersionConflict is one pair of versions of the same package that a project
// resolved AT THE SAME TIME.
//
// It is the shape of dependency hell and it is the fact that usually explains
// "why does this not work": a resolver that installed two versions of one
// library has already told you where to look. Nothing collected it, though
// the data has been there all along — 160 project-package pairs in production
// carry two versions or more.
//
// Failing is the half that makes it worth reading. A pair nobody ever saw
// break is a coexistence, not a conflict, and saying otherwise would turn a
// normal resolution into an accusation.
type VersionConflict struct {
	Lower    string
	Higher   string
	Projects int
	Failing  int
}

// conflictsFromProjectVersions turns per-project version sightings into the
// pairs that coexisted.
//
// A project bucket rotates monthly, so the same bucket IS the same project
// for as long as it exists and two buckets are never merged. Pairs are
// unordered and deduplicated: 7.5.0 beside 8.19.0 is one fact, not two.
func conflictsFromProjectVersions(byProject map[string]map[string]bool, failedIn map[string]bool) []VersionConflict {
	type pair struct{ lo, hi string }
	seen := map[pair]*VersionConflict{}
	for project, versions := range byProject {
		if len(versions) < 2 {
			continue
		}
		list := make([]string, 0, len(versions))
		for v := range versions {
			list = append(list, v)
		}
		sort.Strings(list)
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				k := pair{list[i], list[j]}
				c := seen[k]
				if c == nil {
					c = &VersionConflict{Lower: k.lo, Higher: k.hi}
					seen[k] = c
				}
				c.Projects++
				if failedIn[project] {
					c.Failing++
				}
			}
		}
	}
	out := make([]VersionConflict, 0, len(seen))
	for _, c := range seen {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Failing != out[j].Failing {
			return out[i].Failing > out[j].Failing
		}
		if out[i].Projects != out[j].Projects {
			return out[i].Projects > out[j].Projects
		}
		if out[i].Lower != out[j].Lower {
			return out[i].Lower < out[j].Lower
		}
		return out[i].Higher < out[j].Higher
	})
	return out
}
