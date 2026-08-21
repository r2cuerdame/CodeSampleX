package web

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Class names that utility CSS frameworks define as bare, page-wide rules —
// and that browser extensions inject into every page they touch.
//
// A reader's filter bar drew all seven of its labels on top of each other for
// days. Our stylesheet was correct and rendered correctly everywhere we could
// look; what broke it was that a decided dimension carried class="fixed", and
// something in that browser defined .fixed { position: fixed }. The labels
// left the flow, every one of them resolved to the same static position, and
// the group's height collapsed under them.
//
// Scoping our own selector could not have saved it. Their rule matched the
// element, not our selector, so the only defence is not to answer to the name.
var utilityClassNames = map[string]string{
	"fixed": "position: fixed", "sticky": "position: sticky",
	"absolute": "position: absolute", "relative": "position: relative",
	"static": "position: static", "hidden": "display: none",
	"invisible": "visibility: hidden", "block": "display: block",
	"inline": "display: inline", "flex": "display: flex",
	"grid": "display: grid", "table": "display: table",
	"contents": "display: contents", "float": "float",
	"truncate": "overflow hidden + ellipsis", "collapse": "visibility: collapse",
	"isolate": "isolation: isolate", "container": "a layout container",
}

var classAttr = regexp.MustCompile(`class="([^"{}]*)"`)

func TestNoTemplateClassAnswersToAUtilityName(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("templates", "*.html"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no templates found: %v", err)
	}

	type hit struct{ file, class, means string }
	var hits []hit
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range classAttr.FindAllStringSubmatch(string(b), -1) {
			for _, c := range strings.Fields(m[1]) {
				if means, bad := utilityClassNames[c]; bad {
					hits = append(hits, hit{filepath.Base(p), c, means})
				}
			}
		}
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].class < hits[j].class })
	for _, h := range hits {
		t.Errorf("%s uses class %q, which utility CSS defines as %s — "+
			"any page-wide rule with that name will move our element", h.file, h.class, h.means)
	}
}
