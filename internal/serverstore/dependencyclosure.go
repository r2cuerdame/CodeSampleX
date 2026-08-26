package serverstore

import (
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The dependency closure is the queue source R2C-89 adds: coordinates that
// exist only because somebody's lockfile resolved onto them.
//
// Every other branch of the expansion query reaches a version through an
// evidence row keyed by that exact purl. An edge child that no batch ever
// reported has none, so it can never become work and its column in the
// compatibility matrix stays blank however long the fleet runs. That blank is
// not a failure; it is the absence of evidence, and this is what turns it into
// a question somebody can answer.
//
// How often that happens depends entirely on the client. Measured against
// production on 2026-08-23: 1,497 edges, 353 distinct children, and ZERO of
// them unobserved — the scanner walks the whole resolved tree and reports a
// batch per package, so today every child already has its own evidence row.
// This branch is therefore a net rather than a firehose: it catches the
// children a scan does not reach — a batch truncated at MaxDependsOnPerBatch,
// an ecosystem whose adapter records edges without enumerating the tree, a
// child dropped as UNKNOWN when a request ran past its registry-lookup cap —
// and it stays empty when nothing is missing, which is the correct reading of
// "아직 관측되지 않았으면".
//
// What it deliberately does NOT do is offer the children that ARE observed but
// unproven (73 of them anchored to a chosen package in the same measurement).
// Those are already reachable: the package-level branch emits them. They used
// to sit near the bottom of it because a carried sighting is weighted 1
// against a chosen one's 1000; R2C-90 answered that where it belonged, by
// adding the resolved demand to that branch's score (authoringResolveWeight),
// rather than by duplicating the coordinate into this one.
//
// Three properties are load-bearing, and each is a bound:
//
//   - The versions are RESOLVED, not guessed. dependency_edge holds what a
//     machine holding the lockfile actually saw, so the coordinate handed out
//     is one that really exists in that combination. Nothing here invents a
//     version from a range.
//
//   - The walk is one edge deep from an ANCHOR, and the anchor is either a
//     package somebody chose (direct evidence) or one the network has already
//     proven. A dependency's own dependencies therefore enter only after that
//     dependency is proven, which is what makes transitive expansion possible
//     without letting a huge tree arrive in one pass — each level costs a
//     verified sample.
//
//   - A cycle cannot spin, because the exit condition is a property of the
//     coordinate rather than of the walk: anything already observed or already
//     proven is not a candidate. A ↔ B yields nothing once either end is
//     answered, and nothing at all while both are observed.
//
// A dependency is CANONICAL: one work item per (ecosystem, name, version)
// however many parents pulled it, scored by the distinct project-days that
// resolved it. Two parents wanting the same child is one question, and
// handing it out twice bills the network twice for one answer.
//
// A package the network already proves at SOME version is deliberately not
// dependency work. That is version breadth, the sibling branch already
// produces it, and counting it here would make the dependency backlog read
// high for a reason that has nothing to do with dependencies.

// authoringDependencyClosureCap bounds how many dependency coordinates one
// scheduling pass will consider.
//
// It matches the candidate window rather than the graph: a pass that produced
// more than a window's worth would spend the extra rows nowhere, and an
// unbounded branch is exactly how one project with a ten-thousand-package
// lockfile takes the whole queue. Ordered by demand first, so the cap drops
// the least-wanted rows rather than an arbitrary slice.
const authoringDependencyClosureCap = 200

// dependencyCandidate is one canonical dependency coordinate and the distinct
// project-days that resolved it.
type dependencyCandidate struct {
	ecosystem string
	name      string
	version   string
	projects  int64
}

// rankDependencyCandidates applies the two bounds and the ordering that both
// stores must agree on: at most authoringSiblingVersionsPerPackage releases of
// one library, then at most authoringDependencyClosureCap rows overall, demand
// first throughout.
//
// The tie-breaks are (projects DESC, version DESC as a STRING, name) because
// that is the ordering PostgreSQL can express in the same query. Sorting
// versions as strings ranks 7.0.3 above 14.0.1, which is wrong as a ranking
// and acceptable as a cap — and a cap the two stores disagree about is worse
// than a cap that picks an imperfect six.
func rankDependencyCandidates(in []dependencyCandidate) []WantedRow {
	sort.Slice(in, func(i, j int) bool {
		a, b := in[i], in[j]
		if a.projects != b.projects {
			return a.projects > b.projects
		}
		if a.version != b.version {
			return a.version > b.version
		}
		if a.ecosystem != b.ecosystem {
			return a.ecosystem < b.ecosystem
		}
		return a.name < b.name
	})
	perPackage := make(map[[2]string]int, len(in))
	out := make([]WantedRow, 0, len(in))
	for _, c := range in {
		key := [2]string{c.ecosystem, c.name}
		if perPackage[key] >= authoringSiblingVersionsPerPackage {
			continue
		}
		perPackage[key]++
		out = append(out, WantedRow{
			Ecosystem: c.ecosystem, Name: c.name, Version: c.version,
			Kind: "DEPENDENCY", Score: c.projects,
		})
		if len(out) >= authoringDependencyClosureCap {
			break
		}
	}
	return out
}

// dependencyClosure is the ranked, bounded view the scheduler hands out.
func (f *Fake) dependencyClosure(observed, chosen, verified map[string]bool,
	nameTargets map[[2]string]map[string]bool) []WantedRow {
	return rankDependencyCandidates(f.dependencyOpen(observed, chosen, verified, nameTargets))
}

// dependencyOpen is the Fake's half of the branch documented above, before any
// bound is applied: the whole backlog, which is what the operations panel
// counts. The caller holds f.mu.
//
// observed is every coordinate evidence names, chosen the subset somebody
// listed in their own manifest, verified the coordinates carrying a passing
// sample, and nameTargets the package names already proven at some version.
func (f *Fake) dependencyOpen(observed, chosen, verified map[string]bool,
	nameTargets map[[2]string]map[string]bool) []dependencyCandidate {
	projects := make(map[dependencyCandidate]map[string]bool)
	for edge, projectDays := range f.edges {
		parent := domain.PURL{Ecosystem: edge.ecosystem, Name: edge.parentName,
			Version: edge.parentVersion}.String()
		if !chosen[parent] && !verified[parent] {
			continue
		}
		child := domain.PURL{Ecosystem: edge.ecosystem, Name: edge.childName,
			Version: edge.childVersion}.String()
		if observed[child] || verified[child] {
			continue
		}
		if len(nameTargets[[2]string{edge.ecosystem, edge.childName}]) > 0 {
			continue
		}
		key := dependencyCandidate{ecosystem: edge.ecosystem, name: edge.childName,
			version: edge.childVersion}
		if projects[key] == nil {
			projects[key] = make(map[string]bool)
		}
		for day := range projectDays {
			projects[key][day] = true
		}
	}
	candidates := make([]dependencyCandidate, 0, len(projects))
	for key, days := range projects {
		key.projects = int64(len(days))
		candidates = append(candidates, key)
	}
	return candidates
}
