package node

import "testing"

// A version the lockfile did not give us is not 0.0.0. Substituting one
// fabricated a release that has never existed and uploaded evidence under
// it: every dependency of a Yarn Berry project — yarn.lock beginning with
// __metadata:, which this parser does not read — was recorded as
// name@0.0.0, and another machine's search could be answered with evidence
// attributed to a version nobody can install.
func TestAnUnreadableVersionIsNotReportedAsZero(t *testing.T) {
	got := toResolved([]lockDep{
		{Name: "axios", Version: "1.19.0", Direct: true},
		{Name: "@types/node", Version: ""}, // parser could not pin it
		{Name: "left-pad", Version: ""},
	}, "yarn.lock")

	if len(got) != 1 {
		t.Fatalf("resolved %d packages, want 1 — unpinned ones must be left out", len(got))
	}
	if got[0].PURL.Name != "axios" || got[0].PURL.Version != "1.19.0" {
		t.Errorf("resolved %s@%s", got[0].PURL.Name, got[0].PURL.Version)
	}
	for _, p := range got {
		if p.PURL.Version == "0.0.0" {
			t.Errorf("%s was reported at a fabricated 0.0.0", p.PURL.Name)
		}
	}
}
