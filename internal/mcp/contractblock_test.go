package mcp

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func resultWith(lines []string, passes int64, pkgs []string) domain.SearchResult {
	return domain.SearchResult{
		Case:     &domain.Case{Goal: "read TOML", Packages: pkgs, Contract: lines},
		Evidence: domain.EvidenceSummary{ContractPasses: passes},
	}
}

// "Proven" is a claim, and it is only true when a contract actually passed.
// A sample carries its assertion list from the moment it is authored; the
// pass arrives later, from a container run, and may never arrive at all.
// Printing the lines before then would present an author's intent as a
// result — the exact substitution this whole network exists to refuse.
func TestNothingIsCalledProvenWithoutAPass(t *testing.T) {
	lines := []string{"load() rejects a text stream"}
	if got := contractBlock(resultWith(lines, 0, []string{"pkg:pypi/tomli@2.4.1"})); got != "" {
		t.Errorf("claimed proof with no contract pass:\n%s", got)
	}
	if got := contractBlock(resultWith(lines, 1, []string{"pkg:pypi/tomli@2.4.1"})); got == "" {
		t.Error("a passed contract printed nothing")
	}
}

// The claims belong to the versions the contract ran against. The delta
// elsewhere in the answer prints only major.minor, so if this block cannot
// name the exact versions, the reader has no way to tell what the claims
// are about — and an unscoped claim is a broader one than was proven.
func TestClaimsAreScopedToTheVersionsThatRan(t *testing.T) {
	lines := []string{"load() rejects a text stream"}
	got := contractBlock(resultWith(lines, 1, []string{"pkg:pypi/tomli@2.4.1"}))
	if !strings.Contains(got, "pkg:pypi/tomli@2.4.1") {
		t.Errorf("the block does not say which versions it is about:\n%s", got)
	}
	if got := contractBlock(resultWith(lines, 1, nil)); got != "" {
		t.Errorf("printed claims with nothing to scope them to:\n%s", got)
	}
}

// A shard already trims to 8 lines and appends the server's own sentinel as
// a ninth. Re-trimming here and counting again would both understate a long
// list and swallow that sentinel, so a 9-element list prints whole.
func TestAServerTrimmedListIsPrintedWhole(t *testing.T) {
	var lines []string
	for i := 0; i < 8; i++ {
		lines = append(lines, "assertion")
	}
	lines = append(lines, "… and 26 more, in the sample itself")

	got := contractBlock(resultWith(lines, 1, []string{"pkg:npm/zod@4.4.3"}))
	if !strings.Contains(got, "and 26 more") {
		t.Errorf("the server's own count was dropped:\n%s", got)
	}
	if strings.Contains(got, "and 1 more") {
		t.Errorf("recomputed a count over the server's sentinel:\n%s", got)
	}
}

// An untrimmed list can only come from a local manifest, and there the
// count is honest because the whole list is in hand.
func TestAnUntrimmedLocalListIsCappedWithATrueCount(t *testing.T) {
	var lines []string
	for i := 0; i < 34; i++ {
		lines = append(lines, "assertion")
	}
	got := contractBlock(resultWith(lines, 1, []string{"pkg:npm/zod@4.4.3"}))
	if !strings.Contains(got, "and 26 more") {
		t.Errorf("wrong remainder for a 34-line list:\n%s", got)
	}
	if n := strings.Count(got, "  - assertion"); n != maxContractLinesShown {
		t.Errorf("printed %d lines, want %d", n, maxContractLinesShown)
	}
}

// Every line is printed as written. A filter that kept only lines starting
// with "assert" would drop the ones that read most like cautions — this
// repo's own canonical example among them.
func TestLinesThatDoNotStartWithAssertAreStillPrinted(t *testing.T) {
	lines := []string{
		"load() strictly requires a binary stream and fails with TypeError on text streams",
		"importing a named export from the CJS-only package throws SyntaxError",
	}
	got := contractBlock(resultWith(lines, 1, []string{"pkg:pypi/tomli@2.4.1"}))
	for _, want := range lines {
		if !strings.Contains(got, want) {
			t.Errorf("dropped a line that does not begin with assert:\n%s", got)
		}
	}
}
