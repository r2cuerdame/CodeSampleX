package httpapi

import "testing"

// NO_WORK with nothing else in it is the answer that cost a farm four and a
// half hours. Three workers polled 80 times, every answer was 200 NO_WORK,
// and the board held 2,961 claimable coordinates the whole time. Nothing on
// either side could say which rule emptied the list, so the only way to find
// out was to read the handler and guess.
//
// The funnel is counts and rule names. No package name, version or path
// leaves the machine in it -- it says how many survived each step, which is
// exactly what tells an operator whether the board is empty or the request is
// being refused.
func TestNoWorkSaysWhereTheCandidatesWent(t *testing.T) {
	funnel := authoringFunnel{
		Wanted: 12, WantedEligible: 0,
		Expansion: 200, ExpansionEligible: 117,
		AfterDependency: 117, AfterUnauthorable: 117, Offered: 117,
	}
	body := funnel.noWork()
	if body["status"] != "NO_WORK" {
		t.Fatalf("status = %v", body["status"])
	}
	f, ok := body["funnel"].(authoringFunnel)
	if !ok {
		t.Fatalf("no funnel in the answer: %v", body)
	}
	if f.Offered != 117 {
		t.Errorf("offered = %d, want the count the claim actually saw", f.Offered)
	}
}

// The reason has to name the step that emptied the list, so the operator does
// not have to diff seven numbers to find the zero.
func TestTheFunnelNamesTheStepThatEmptiedIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		funnel authoringFunnel
		want   string
	}{
		{"board empty", authoringFunnel{}, "no candidates"},
		{"request refused everything", authoringFunnel{Expansion: 200, Wanted: 5}, "no candidate is eligible for this request"},
		{"registry dropped them", authoringFunnel{Expansion: 200, ExpansionEligible: 117}, "dropped as unauthorable"},
		{"all claimed", authoringFunnel{Expansion: 200, ExpansionEligible: 117,
			AfterDependency: 117, AfterUnauthorable: 117, Offered: 117}, "already claimed or already authored"},
	} {
		if got := tc.funnel.reason(); got != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.want)
		}
	}
}
