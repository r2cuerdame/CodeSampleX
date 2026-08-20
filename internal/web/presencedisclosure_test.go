package web

import (
	"strings"
	"testing"
)

// The coverage disclosure published one skew — everything observed on
// Windows, everything proven on Linux — and left a larger one unsaid: most
// of what this network knows about, it has only ever seen INSTALLED. In
// production 1,167 npm versions carried presence records and had never once
// been exercised, while the front page said 1,888 packages had evidence.
//
// An observatory's failure is not a false claim, it is an unstated skew.
func TestCoverageDisclosureSaysWhatWasNeverExercised(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.presence = []PresenceCoverage{
		{Ecosystem: "npm", PresenceOnly: 1167, Exercised: 1122},
		{Ecosystem: "golang", PresenceOnly: 1, Exercised: 324},
	}
	body := get(t, mux, "/").Body.String()

	mustContain(t, body, "1,167")
	mustContain(t, body, "npm")
	// Stated as a share of what that ecosystem holds, because 1,167 alone
	// says nothing about whether it is most of them or a rounding error.
	if !strings.Contains(body, "51%") {
		t.Errorf("the npm share of never-exercised packages is missing")
	}
}

// An ecosystem the network has genuinely exercised must not be listed as a
// gap: a disclosure that cries skew everywhere is one nobody reads.
func TestCoverageDisclosureOmitsFullyExercisedEcosystems(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.presence = []PresenceCoverage{
		{Ecosystem: "golang", PresenceOnly: 0, Exercised: 324},
	}
	body := get(t, mux, "/").Body.String()
	if strings.Contains(body, `class="presencegap"`) {
		t.Error("a fully exercised ecosystem was reported as a coverage gap")
	}
}
