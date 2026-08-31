package adapters

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// Which registered adapters can report a dependency tree, asserted by name.
//
// ResolvedEdges asks each adapter whether it is an EdgeScanner and skips the
// ones that are not. That skip is silent by design — an ecosystem with no
// lockfile to read should contribute nothing rather than a guess — so an
// adapter that MEANT to report edges and does not looks exactly like one that
// never intended to. Production held 8,620 edges on 2026-08-31 with zero from
// golang, and nothing anywhere failed.
//
// The way it breaks is not a missing file. It is a method set: goadapter's
// methods take a pointer receiver and All() registers goadapter.New(), so a
// ScanEdges declared on the value receiver still satisfies the interface,
// while the reverse does not. That is invisible at the call site and compiles
// either way.
//
// So the expectation is pinned here as a list. An ecosystem gaining an edge
// scanner has to say so; one losing it fails.
func TestWhichRegisteredAdaptersCanReportATree(t *testing.T) {
	canScanEdges := map[string]bool{
		"npm":    true,
		"pypi":   true,
		"golang": true,
		"cargo":  true,
	}
	seen := map[string]bool{}
	for _, a := range All() {
		eco := a.Ecosystem()
		seen[eco] = true
		_, ok := a.(scanner.EdgeScanner)
		want, listed := canScanEdges[eco]
		if !listed {
			t.Errorf("adapter %q is registered but this test does not say whether it reports a tree", eco)
			continue
		}
		if ok != want {
			t.Errorf("adapter %q: EdgeScanner=%v, want %v", eco, ok, want)
		}
	}
	for eco := range canScanEdges {
		if !seen[eco] {
			t.Errorf("this test expects adapter %q, which is not registered", eco)
		}
	}
}
