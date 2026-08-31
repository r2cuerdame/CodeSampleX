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

// The four axes must read as sentences, not as a squeezed column.
//
// Twice on this site a text block has been placed in a grid without being told
// which column it belongs to, landed in the narrow one, and shipped wrapping
// four words to a line. Both times the stylesheet looked right and the
// rendered page did not, so this is measured before it goes out rather than
// found on the deployed page.
func TestTheCompatibilityAxesAreReadableAndDoNotOverflow(t *testing.T) {
	chrome := findChrome(t)
	mux, store := newTestMux(t, nil)
	store.packages = []PackageHit{{
		Ecosystem: "npm", Name: "@some-organisation/a-long-unbroken-package-name",
		LatestVersion: "10.20.30-rc.1", EvidenceCount: 12,
	}}
	srv := httptest.NewServer(compatHarness(mux, "/compatibility?lang=en"))
	defer srv.Close()

	var reports []compatLayoutReport
	payload := renderMeasurement(t, chrome, srv.URL+compatMeasurePath)
	if err := json.Unmarshal([]byte(payload), &reports); err != nil {
		t.Fatalf("measurement %q: %v", payload, err)
	}
	for _, r := range reports {
		if r.Axes == 0 {
			t.Errorf("viewport %dpx: no axes rendered", r.Width)
			continue
		}
		if r.Axes != 4 {
			t.Errorf("viewport %dpx: %d axes rendered, want 4", r.Width, r.Axes)
		}
		if r.ScrollWidth > r.ClientWidth {
			t.Errorf("viewport %dpx: document is %.0fpx wide in a %.0fpx viewport — the page scrolls sideways",
				r.Width, r.ScrollWidth, r.ClientWidth)
		}
		// An axis answer squeezed into its label's width is the failure this
		// exists for: the label column is 5.5rem, so anything near that is
		// text wrapping a word at a time.
		if r.NarrowText < 96 {
			// The computed values come with it: the first cause here was a
			// grid that never applied, because `.pkglist li` is more specific
			// than a bare `.record-axis` and kept the items as flex.
			t.Errorf("viewport %dpx: an axis answer is only %.0fpx wide — it is wrapping inside the label column (display=%s cols=%s sheets=%d)",
				r.Width, r.NarrowText, r.Display, r.Cols, r.Sheets)
		}
	}
}

var compatViewports = []int{360, 900, 1280}

type compatLayoutReport struct {
	Width       int     `json:"width"`
	Axes        int     `json:"axes"`
	ScrollWidth float64 `json:"scrollWidth"`
	ClientWidth float64 `json:"clientWidth"`
	NarrowText  float64 `json:"narrowText"`
	Display     string  `json:"display"`
	Cols        string  `json:"cols"`
	Sheets      int     `json:"sheets"`
}

const compatMeasurePath = "/__compat-layout-measure"

func compatHarness(mux *http.ServeMux, target string) http.Handler {
	widths := make([]string, 0, len(compatViewports))
	frames := make([]string, 0, len(compatViewports))
	for _, w := range compatViewports {
		widths = append(widths, fmt.Sprint(w))
		frames = append(frames, fmt.Sprintf(
			`<iframe id="c%d" width="%d" height="900" src="%s"></iframe>`, w, w, html.EscapeString(target)))
	}
	page := strings.NewReplacer(
		"__WIDTHS__", strings.Join(widths, ","),
		"__FRAMES__", strings.Join(frames, "\n"),
	).Replace(compatHarnessHTML)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != compatMeasurePath {
			mux.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
}

const compatHarnessHTML = `<!doctype html>
<meta charset="utf-8"><title>compatibility layout measure</title>
<body style="margin:0">
__FRAMES__
<pre id="measure">PENDING</pre>
<script>
function run(){
  var out = [];
  var widths = [__WIDTHS__];
  for (var i = 0; i < widths.length; i++) {
    var w = widths[i], fr = document.getElementById('c' + w);
    var doc = fr.contentDocument;
    var axes = doc.querySelectorAll('.record-axis');
    var narrow = 1e9;
    for (var j = 0; j < axes.length; j++) {
      var t = axes[j].querySelector('.record-axis__text');
      if (!t) continue;
      var tw = t.getBoundingClientRect().width;
      if (tw < narrow) narrow = tw;
    }
    if (axes.length === 0) narrow = 0;
    var ul = doc.querySelector('.record-axes');
    var win = fr.contentWindow;
    out.push({width: w, axes: axes.length,
              scrollWidth: doc.documentElement.scrollWidth,
              clientWidth: doc.documentElement.clientWidth,
              narrowText: narrow,
              display: ul ? win.getComputedStyle(ul).display : 'none',
              cols: ul ? win.getComputedStyle(ul).gridTemplateColumns : '',
              sheets: doc.styleSheets.length});
  }
  document.getElementById('measure').textContent = JSON.stringify(out);
}
window.addEventListener('load', function(){ setTimeout(run, 400); });
</script>
`
