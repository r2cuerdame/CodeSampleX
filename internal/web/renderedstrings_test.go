package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A string that exists, translates into all nine locales, and never reaches a
// screen is invisible to every check this repo has except reading the page.
//
// Measured on 2026-08-30: 116 of 446 keys were referenced by nothing outside
// the locale files. Among them were all seven landing.value_* strings — the
// section literally titled "What you get" — so the front page carried its own
// answer to "what do I get out of this" the whole time and never showed it.
// cube.sub had been in the same state earlier, which is what makes this a
// shape rather than an accident.
//
// The guard is scoped to landing.* because that is the surface where an
// unrendered string is a product defect rather than leftovers: a retired page
// leaves keys behind and cleaning those up is bookkeeping, but a landing
// string nobody renders is a promise the page does not keep.
func TestEveryLandingStringReachesTheScreen(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("i18n", "locales", "en.json"))
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]string
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}

	var src strings.Builder
	for _, dir := range []string{"templates", "."} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if !strings.HasSuffix(name, ".html") && !strings.HasSuffix(name, ".go") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			src.Write(b)
		}
	}
	body := src.String()

	var unrendered, revived []string
	for k := range keys {
		if !strings.HasPrefix(k, "landing.") {
			continue
		}
		if strings.Contains(body, k) {
			if _, known := knownUnrendered[k]; known {
				revived = append(revived, k)
			}
			continue
		}
		if _, known := knownUnrendered[k]; !known {
			unrendered = append(unrendered, k)
		}
	}
	sort.Strings(unrendered)
	if len(unrendered) > 0 {
		t.Errorf("%d NEW landing strings are translated into every locale and rendered by nothing:\n  %s\n"+
			"Either render them or delete them; a promise the page does not keep is worse than no promise.",
			len(unrendered), strings.Join(unrendered, "\n  "))
	}
	sort.Strings(revived)
	if len(revived) > 0 {
		t.Logf("now rendered, remove from knownUnrendered:\n  %s", strings.Join(revived, "\n  "))
	}
}

// knownUnrendered is the backlog this test froze rather than deleted.
//
// These are written and translated into all nine locales for sections the
// landing page does not have: a flywheel, a confidence ladder, a comparison
// against docs and community answers, a samples collection, a worked example.
// They are #86's raw material — that issue restructures the homepage around
// Findings, Samples and the dependency map — so deleting them here would throw
// away copy somebody wrote for work that is still open.
//
// The list is a ratchet, not permission. It may shrink and must not grow: a
// NEW unrendered landing string fails this test, and the seven
// landing.value_* keys that used to be here are gone because they are on the
// page now.
var knownUnrendered = map[string]struct{}{
	"landing.diff_code":        {},
	"landing.diff_code_k":      {},
	"landing.diff_community":   {},
	"landing.diff_community_k": {},
	"landing.diff_csx":         {},
	"landing.diff_docs":        {},
	"landing.diff_docs_k":      {},
	"landing.example_open":     {},
	"landing.example_result":   {},
	"landing.flywheel_1":       {},
	"landing.flywheel_2":       {},
	"landing.flywheel_3":       {},
	"landing.flywheel_4":       {},
	"landing.flywheel_heading": {},
	"landing.ladder_cross":     {},
	"landing.ladder_heading":   {},
	"landing.ladder_multi":     {},
	"landing.ladder_observed":  {},
	"landing.ladder_stable":    {},
	"landing.ladder_sub":       {},
	"landing.ladder_verified":  {},
	"landing.llm_link":         {},
	"landing.network_heading":  {},
	"landing.samples_heading":  {},
	"landing.samples_sub":      {},
	"landing.scope_note":       {},
	"landing.see_matrix":       {},
	"landing.tested_line":      {},
}
