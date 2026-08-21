package evidence

import (
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// publicEdges keeps the dependency edges whose BOTH ends are public, grouped
// by parent purl.
//
// An edge between two public packages is already registry information: npm
// will tell anyone that a@1.2.0 depends on b. One end private makes it a fact
// about somebody's private code, so it does not travel — the same rule
// packages follow, applied here where the edges are chosen rather than a
// second time further down, because a rule enforced twice is a rule that
// drifts.
func publicEdges(edges []scanner.Edge, public map[string]domain.PURL) map[string][]string {
	seen := map[string]map[string]bool{}
	for _, e := range edges {
		parent, child := e.Parent.String(), e.Child.String()
		if _, ok := public[parent]; !ok {
			continue
		}
		if _, ok := public[child]; !ok {
			continue
		}
		if seen[parent] == nil {
			seen[parent] = map[string]bool{}
		}
		seen[parent][child] = true
	}
	out := make(map[string][]string, len(seen))
	for parent, children := range seen {
		list := make([]string, 0, len(children))
		for c := range children {
			list = append(list, c)
		}
		sort.Strings(list)
		// Clamped to the wire cap: the server refuses a longer list, and a
		// refused batch loses the observation riding in it. Sorted first, so
		// which entries survive is deterministic.
		if len(list) > domain.MaxDependsOnPerBatch {
			list = list[:domain.MaxDependsOnPerBatch]
		}
		out[parent] = list
	}
	return out
}
