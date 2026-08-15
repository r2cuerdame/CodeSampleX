package search

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Found live, against the real network: asking
//
//	"protobuf backend upb vs python on alpine"
//
// from an empty directory returned a python-dateutil sample about parsing
// ambiguous date strings, graded COMPATIBLE with a score of 0.79.
//
// The package name python-dateutil tokenizes to {python, dateutil}, and
// every part was treated as a STRONG identifier — the class of token that
// means "the question named this library". So the word "python" in any
// question matched it, and one strong token is enough to open the
// relevance gate on its own. The same hole is under go-*, node-*, rust-*,
// java-*, php-* and every other language-prefixed name, which is a large
// share of every registry.
//
// A wrong HIT is worse than a MISS, and this one arrived confident.
func TestALanguageNameInsideAPackageNameIsNotNamingThePackage(t *testing.T) {
	dateutil := &candidate{
		caseObj: &domain.Case{
			Goal:     "Parse an ambiguous date string in Python without silently getting the wrong day",
			Packages: []string{"pkg:pypi/python-dateutil@2.9.0.post0"},
		},
		packages: parsePURLs([]string{"pkg:pypi/python-dateutil@2.9.0.post0"}),
	}

	const q = "protobuf backend upb vs python on alpine"
	if strong, _ := intentSignal(q, dateutil); strong > 0 {
		t.Errorf("the word %q counted as naming python-dateutil (strong=%d)", "python", strong)
	}
	if aboutTheSameThing(q, dateutil) {
		t.Error("a question about protobuf was judged to be about python-dateutil")
	}
}

// Naming the library itself must still be the strongest signal there is.
func TestNamingThePackageStillCounts(t *testing.T) {
	dateutil := &candidate{
		caseObj: &domain.Case{
			Goal:     "Parse an ambiguous date string in Python without silently getting the wrong day",
			Packages: []string{"pkg:pypi/python-dateutil@2.9.0.post0"},
		},
		packages: parsePURLs([]string{"pkg:pypi/python-dateutil@2.9.0.post0"}),
	}
	for _, q := range []string{
		"dateutil parse ambiguous date",
		"python-dateutil dayfirst",
	} {
		if strong, _ := intentSignal(q, dateutil); strong == 0 {
			t.Errorf("%q did not count as naming the package", q)
		}
	}

	// And a package whose whole name is a language word is still named by
	// that word — it is the identifier, not a prefix.
	golang := &candidate{
		caseObj:  &domain.Case{Goal: "run something", Packages: []string{"pkg:npm/go@1.0.0"}},
		packages: parsePURLs([]string{"pkg:npm/go@1.0.0"}),
	}
	if strong, _ := intentSignal("go package usage", golang); strong == 0 {
		t.Error("a package literally named go is no longer nameable")
	}
}

// A short package name must be matched as a WORD, not a substring: a plain
// contains test made a package named "go" nameable by any question about
// mongodb, django or google.
func TestAShortNameIsNotMatchedInsideAnotherWord(t *testing.T) {
	golang := &candidate{
		caseObj:  &domain.Case{Goal: "run something", Packages: []string{"pkg:npm/go@1.0.0"}},
		packages: parsePURLs([]string{"pkg:npm/go@1.0.0"}),
	}
	for _, q := range []string{
		"mongodb connection pooling",
		"django orm select related",
		"google cloud storage upload",
	} {
		if strong, _ := intentSignal(q, golang); strong > 0 {
			t.Errorf("%q was read as naming the package \"go\"", q)
		}
	}
}
