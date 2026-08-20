package web

import "testing"

// Authors name the same API three ways, and production carries all three
// for a single package: of pgx v5.10.0's 294 symbol chips, 192 were written
// as the full module path plus the member, most of the rest as the import
// alias plus the member, and only a handful bare. An exact string match
// claimed the last spelling only, so /…/v5.10.0/Batch showed one of the ten
// samples that answer for Batch and the other nine piled up on the version
// page — which is the pile this matcher exists to break up.
func TestSampleNamesSymbolAcceptsEverySpelling(t *testing.T) {
	for _, named := range []string{
		"Batch",
		"pgx.Batch",
		"github.com/jackc/pgx/v5.Batch",
		"  github.com/jackc/pgx/v5.Batch  ",
	} {
		if !sampleNamesSymbol([]string{named}, "Batch") {
			t.Errorf("sampleNamesSymbol([%q], %q) = false, want true", named, "Batch")
		}
	}
}

// The page's own symbol can be qualified too, depending on what the
// ecosystem's extractor recorded, so the leaf is taken from both sides.
func TestSampleNamesSymbolMatchesWhenThePageSymbolIsQualified(t *testing.T) {
	if !sampleNamesSymbol([]string{"pgx.Batch"}, "github.com/jackc/pgx/v5.Batch") {
		t.Error("qualified page symbol did not match an aliased sample symbol")
	}
}

// Matching on the leaf must not become matching on a prefix: Batch and
// BatchResults are different APIs and a reader following a link to one must
// not be handed samples for the other.
func TestSampleNamesSymbolRejectsADifferentMember(t *testing.T) {
	for _, named := range []string{
		"pgx.BatchResults",
		"github.com/jackc/pgx/v5.Identifier",
		"BatchTracer",
		"",
	} {
		if sampleNamesSymbol([]string{named}, "Batch") {
			t.Errorf("sampleNamesSymbol([%q], %q) = true, want false", named, "Batch")
		}
	}
	if sampleNamesSymbol([]string{"Batch"}, "") {
		t.Error("an empty page symbol matched")
	}
}
