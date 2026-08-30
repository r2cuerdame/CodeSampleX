package web

import (
	"regexp"
	"strings"
	"testing"
)

var moreHref = regexp.MustCompile(`<a class="small" href="([^"]+)">`)

// The front page teases findings and then links somewhere they are not.
//
// homeFindings draws from the documented and believed groups — the
// hand-checked ones. The "All measured findings" link goes to /findings, which
// defaults to the tab those entries are absent from, so a reader who clicks
// the thing they just read about lands on a page that does not contain it.
func TestTheHomeTeaserLinksWhereItsFindingsActuallyAre(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	home := get(t, mux, "/").Body.String()

	i := strings.Index(home, `class="homefindings`)
	if i < 0 {
		t.Skip("this fixture renders no home findings")
	}
	m := moreHref.FindStringSubmatch(home[i:])
	if m == nil {
		t.Fatal("the findings teaser has no link out")
	}
	href := m[1]

	// Whatever it teased has to be on the page it points at.
	teased := regexp.MustCompile(`href="(/findings[^"]*#[^"]+)"`).FindStringSubmatch(home[i:])
	dest := get(t, mux, href).Body.String()
	if teased != nil {
		anchor := teased[1][strings.Index(teased[1], "#")+1:]
		if !strings.Contains(dest, `id="`+anchor+`"`) {
			t.Errorf("the teaser links to %q, which does not contain the finding it teased (%s)", href, anchor)
		}
	}
	if !strings.Contains(href, "tab=curated") {
		t.Errorf("the teaser links to %q; the entries it shows live on the hand-checked tab", href)
	}

	// The link now carries a query of its own, so the locale has to join it
	// rather than start a second one.
	ko := get(t, mux, "/?lang=ko").Body.String()
	if j := strings.Index(ko, `class="homefindings`); j >= 0 {
		if m := moreHref.FindStringSubmatch(ko[j:]); m != nil {
			if strings.Contains(m[1], "?lang=ko") || !strings.Contains(m[1], "lang=ko") {
				t.Errorf("localized teaser link is %q, want the locale appended to the existing query", m[1])
			}
		}
	}
}
