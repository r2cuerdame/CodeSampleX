package i18n

import (
	"strings"
	"testing"
)

// T is positional Sprintf: every locale's format string consumes its
// arguments in CALL order, whatever order the sentence puts them in.
// findings.go passes (from, to, total) — and three locales had written the
// slots as (total, from, to), so page two in Japanese read "of the 26
// measured, items 50–133": the same backwards-count defect
// pkg.clusters_truncated was already fixed for.
// A locale that ships the English sentence verbatim is a translation gap
// wearing a translated key: the fallback would render the same text, and the
// page reads as broken to exactly the visitor the locale exists for.
func TestAgentsManualIsTranslatedEverywhere(t *testing.T) {
	en := T("en", "landing.agents_manual")
	if en == "" {
		t.Fatal("landing.agents_manual is missing entirely")
	}
	for _, lang := range Supported {
		if lang == "en" {
			continue
		}
		if T(lang, "landing.agents_manual") == en {
			t.Errorf("%s ships the English text for landing.agents_manual", lang)
		}
	}
}

func TestFindingsRangeConsumesArgsInCallOrder(t *testing.T) {
	for _, lang := range Supported {
		got := T(lang, "findings.range", "26", "50", "133")
		if !strings.Contains(got, "26–50") {
			t.Errorf("%s: %q does not show items 26–50", lang, got)
		}
		if !strings.Contains(got, "133") {
			t.Errorf("%s: %q lost the total", lang, got)
		}
		if strings.Contains(got, "50–133") {
			t.Errorf("%s: %q reads the count backwards", lang, got)
		}
	}
}
