package i18n

import "testing"

// The site printed "1 findings across 1 ecosystems" whenever a filter narrowed
// to one, and "1 tools", and "1 anonymous daily reports" dozens of times down a
// package page. Every count was a raw number concatenated with one hardcoded
// noun, because nothing here could inflect one.
//
// The forms are the CLDR ones for the locales this site ships. English, German,
// Spanish and Portuguese split at one; French counts zero as singular; Russian
// takes three forms and the rule is on the last digits, so 11 is "many" while 1
// and 21 are "one".
func TestPluralFormFollowsTheLocalesRule(t *testing.T) {
	cases := []struct {
		lang string
		n    int64
		want string
	}{
		{"en", 0, "other"}, {"en", 1, "one"}, {"en", 2, "other"}, {"en", 21, "other"},
		{"de", 1, "one"}, {"de", 5, "other"},
		{"es", 1, "one"}, {"es", 0, "other"},
		{"pt-BR", 1, "one"}, {"pt-BR", 3, "other"},
		// French counts zero with the singular.
		{"fr", 0, "one"}, {"fr", 1, "one"}, {"fr", 2, "other"},
		// Russian: one=1,21,31 · few=2-4,22-24 · many=0,5-20,11-14
		{"ru", 1, "one"}, {"ru", 21, "one"}, {"ru", 11, "many"},
		{"ru", 2, "few"}, {"ru", 23, "few"}, {"ru", 12, "many"},
		{"ru", 5, "many"}, {"ru", 0, "many"},
		// No inflection at all in these.
		{"ko", 1, "other"}, {"ja", 1, "other"}, {"zh-CN", 1, "other"},
	}
	for _, c := range cases {
		if got := pluralForm(c.lang, c.n); got != c.want {
			t.Errorf("pluralForm(%q, %d) = %q, want %q", c.lang, c.n, got, c.want)
		}
	}
}

// A count and its noun travel together, because splitting them is what let the
// number and the word disagree in the first place.
func TestPluralRendersTheCountWithTheRightNoun(t *testing.T) {
	if got := Plural("en", "findings.n_findings", 1); got != "1 finding" {
		t.Errorf("en 1 = %q, want \"1 finding\"", got)
	}
	if got := Plural("en", "findings.n_findings", 194); got != "194 findings" {
		t.Errorf("en 194 = %q, want \"194 findings\"", got)
	}
	// The number is formatted for the locale, not printed raw.
	if got := Plural("de", "findings.n_findings", 1234); got != "1.234 Befunde" {
		t.Errorf("de 1234 = %q, want the locale's group separator", got)
	}
	// A locale with no inflection still gets its own noun.
	if got := Plural("ko", "findings.n_findings", 1); got == "" {
		t.Error("ko rendered nothing")
	}
}

// A key with no plural variants must not vanish. Rendering nothing is how a
// missing entry becomes an invisible gap instead of a visible defect.
func TestPluralFallsBackRatherThanRenderingNothing(t *testing.T) {
	if got := Plural("en", "definitely.not.a.key", 3); got == "" {
		t.Error("an unknown plural key rendered nothing")
	}
}
