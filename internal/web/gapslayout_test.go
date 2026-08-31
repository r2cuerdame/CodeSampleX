package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The gap list must not push the page sideways on a phone.
//
// Every row carries three axis lines laid out as a two-column grid with a
// fixed first column, plus an unaskable reason that is a full English
// sentence, plus package names that can be long and unbreakable. Any one of
// those can widen the document past the viewport, and a page that scrolls
// horizontally on a phone is a page whose right-hand column nobody reads.
//
// Measured rather than read off the stylesheet: whether a box overflows is a
// question about the rendered layout, and a media query cannot be inspected
// for the answer. The narrow-screen rule that collapses the axis grid to one
// column is exactly the kind of thing that looks right in the source and is
// wrong at 320px.
func TestTheGapListDoesNotScrollSidewaysOnAPhone(t *testing.T) {
	chrome := findChrome(t)
	mux, store := newTestMux(t, nil)
	store.gaps = []CompletenessGap{
		// The widest shapes the page can actually be handed: a long scoped
		// name, and a reason sentence that cannot be wrapped by the author.
		{Ecosystem: "npm", Name: "@some-organisation/a-very-long-unbroken-package-name-here",
			Version: "10.20.30-rc.1", HasEvidence: true, Dependency: GapDependencyUnknown,
			SampleNAReason: "npm per-platform native build: what a sample would import is the .node binary its parent selects"},
		{Ecosystem: "golang", Name: "example.com/module/with/a/deep/import/path",
			Version: "v1.2.3", HasEvidence: true, Dependency: GapDependencyUnknown,
			DependencyNAReason: "no dependency scanner ships for golang: the tree is unread, not empty"},
	}
	srv := httptest.NewServer(gapsHarness(mux, "/gaps"))
	defer srv.Close()

	var reports []gapLayoutReport
	payload := renderMeasurement(t, chrome, srv.URL+gapsMeasurePath)
	if err := json.Unmarshal([]byte(payload), &reports); err != nil {
		t.Fatalf("measurement %q: %v", payload, err)
	}
	if len(reports) != len(gapViewports) {
		t.Fatalf("measured %d viewports, want %d", len(reports), len(gapViewports))
	}
	for _, r := range reports {
		if r.Rows == 0 {
			t.Errorf("viewport %dpx: no gap rows rendered", r.Width)
			continue
		}
		if r.ScrollWidth > r.ClientWidth {
			t.Errorf("viewport %dpx: document is %.0fpx wide in a %.0fpx viewport — the page scrolls sideways",
				r.Width, r.ScrollWidth, r.ClientWidth)
		}
		if r.WidestRow > r.ClientWidth {
			t.Errorf("viewport %dpx: a gap row is %.0fpx wide in a %.0fpx viewport",
				r.Width, r.WidestRow, r.ClientWidth)
		}
	}
}

// gapViewports covers the narrow widths the one-column rule applies at, plus a
// desktop width where the two-column axis grid is in force. They are different
// code paths and neither may overflow.
var gapViewports = []int{320, 390, 900}

type gapLayoutReport struct {
	Width       int     `json:"width"`
	Rows        int     `json:"rows"`
	ScrollWidth float64 `json:"scrollWidth"`
	ClientWidth float64 `json:"clientWidth"`
	WidestRow   float64 `json:"widestRow"`
}

const gapsMeasurePath = "/__gaps-layout-measure"

func gapsHarness(mux *http.ServeMux, target string) http.Handler {
	widths := make([]string, 0, len(gapViewports))
	frames := make([]string, 0, len(gapViewports))
	for _, w := range gapViewports {
		widths = append(widths, fmt.Sprint(w))
		frames = append(frames, fmt.Sprintf(
			`<iframe id="g%d" width="%d" height="900" src="%s"></iframe>`, w, w, html.EscapeString(target)))
	}
	page := strings.NewReplacer(
		"__WIDTHS__", strings.Join(widths, ","),
		"__FRAMES__", strings.Join(frames, "\n"),
	).Replace(gapsHarnessHTML)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gapsMeasurePath {
			mux.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
}

const gapsHarnessHTML = `<!doctype html>
<meta charset="utf-8"><title>gaps layout measure</title>
<body style="margin:0">
__FRAMES__
<pre id="measure">PENDING</pre>
<script>
function run(){
  var out = [];
  var widths = [__WIDTHS__];
  for (var i = 0; i < widths.length; i++) {
    var w = widths[i], fr = document.getElementById('g' + w);
    var doc = fr.contentDocument;
    var rows = doc.querySelectorAll('.gaprow');
    var widest = 0;
    for (var j = 0; j < rows.length; j++) {
      var r = rows[j].getBoundingClientRect();
      if (r.right > widest) widest = r.right;
    }
    out.push({width: w, rows: rows.length,
              scrollWidth: doc.documentElement.scrollWidth,
              clientWidth: doc.documentElement.clientWidth,
              widestRow: widest});
  }
  document.getElementById('measure').textContent = JSON.stringify(out);
}
window.addEventListener('load', function(){ setTimeout(run, 400); });
</script>
`
