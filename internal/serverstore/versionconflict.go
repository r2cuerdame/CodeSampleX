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

// conflictsFromProjectVersions turns per-project-day version sightings into
// the pairs that collided.
//
// The key is the project bucket AND the day, not the bucket alone. A bucket
// lasts a month, so grouping by it called every upgrade a conflict: a project
// that moved from 7.5.0 to 8.19.0 in August looked identical to one that held
// both. A day is the narrowest window the server has — it cannot tell one
// lockfile from three builds in an afternoon — so this says "the same project
// used both on the same day", which is what the data supports.
//
// Pairs are unordered and deduplicated: 7.5.0 beside 8.19.0 is one fact, not
// two. Projects counts the project-days, so one project seen twice is two.
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
		// A pair nobody ever saw break is a coexistence. Two versions of one
		// library in one project is ordinary — npm nests duplicates by design
		// — and returning those would make the caller decide what a conflict
		// is, which is how one rule becomes two that drift apart.
		if c.Failing == 0 {
			continue
		}
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
