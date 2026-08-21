package evidence

import (
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// coresidentVersions reports, per purl, the OTHER versions of the same
// library present in the same resolution.
//
// One library installed at two versions is the commonest reason a build does
// not work, and the server cannot see it. An ObservationBatch carries a
// single package, so a lockfile arrives already shredded into independent
// records; the finest grouping left is a project and a day, and a project
// that runs two builds in an afternoon against different lockfiles produces
// exactly the input that would be read as a collision. Pairing there is
// inference.
//
// The scanner holds the whole lockfile at once. Here it is an observation.
//
// The key is ecosystem AND name: gem/rack and npm/rack are different
// libraries and pairing their versions would invent a conflict. Only public
// packages reach this — the caller filters — and only a version string
// travels, which is the same shape of fact the package name already is.
func coresidentVersions(packages []domain.PURL) map[string][]string {
	type libKey struct{ ecosystem, name string }
	versions := map[libKey]map[string]bool{}
	for _, p := range packages {
		if p.Version == "" {
			continue
		}
		k := libKey{p.Ecosystem, p.Name}
		if versions[k] == nil {
			versions[k] = map[string]bool{}
		}
		versions[k][p.Version] = true
	}
	out := map[string][]string{}
	for _, p := range packages {
		if p.Version == "" {
			continue
		}
		seen := versions[libKey{p.Ecosystem, p.Name}]
		if len(seen) < 2 {
			continue
		}
		others := make([]string, 0, len(seen)-1)
		for v := range seen {
			if v != p.Version {
				others = append(others, v)
			}
		}
		sort.Strings(others)
		// Clamped to the wire cap: the server refuses a longer list, and a
		// refused batch loses the observation riding in it. Sorted first, so
		// which entries survive is deterministic.
		if len(others) > domain.MaxCoresidentPerBatch {
			others = others[:domain.MaxCoresidentPerBatch]
		}
		out[p.String()] = others
	}
	return out
}
