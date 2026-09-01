package adapters

import (
	"sort"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// The public site may not say a scanner is missing when one ships.
//
// domain.DependencyNotApplicable decides whether a release's dependency axis
// is "nobody has looked yet" or "nothing here can ever look", and /gaps, the
// package page and /compatibility all render that answer. It held a
// hardcoded list of ecosystems, and the list stopped matching the adapters:
// goadapter grew an EdgeScanner and the list did not, so every Go release on
// the site said "no dependency scanner ships for golang" while the scanner
// was in the binary serving the page.
//
// This is the cross-check the taxonomy did not have. It derives the truth
// from the adapters actually registered and fails when the two disagree in
// either direction — a scanner that ships and is not claimed, or a claim with
// no scanner behind it.
func TestTheDependencyTaxonomyMatchesTheRegisteredAdapters(t *testing.T) {
	scans := map[string]bool{}
	for _, a := range All() {
		if _, ok := a.(scanner.EdgeScanner); ok {
			scans[a.Ecosystem()] = true
		}
	}
	if len(scans) == 0 {
		t.Fatal("no registered adapter implements EdgeScanner; the check has nothing to compare")
	}

	var wrong []string
	for eco := range scans {
		if reason, notApplicable := domain.DependencyNotApplicable(eco); notApplicable {
			wrong = append(wrong, eco+": ships an EdgeScanner, but the site says "+reason)
		}
	}
	for _, a := range All() {
		eco := a.Ecosystem()
		if scans[eco] {
			continue
		}
		if _, notApplicable := domain.DependencyNotApplicable(eco); !notApplicable {
			wrong = append(wrong, eco+": claimed scannable, but no registered adapter implements EdgeScanner")
		}
	}
	sort.Strings(wrong)
	if len(wrong) > 0 {
		t.Errorf("the dependency taxonomy and the adapters disagree:\n  %s", strings.Join(wrong, "\n  "))
	}
}
