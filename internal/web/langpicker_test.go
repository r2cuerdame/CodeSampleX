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

// Opening the language picker must not resize the header.
//
// The header is sticky: whatever height it has, it keeps, on every page and at
// every scroll position. A panel rendered in normal flow inside the nav grows
// the bar the moment somebody opens it, so the act of looking for your own
// language takes a bite out of the page you were reading — and on a phone, a
// nine-item list in flow takes most of the screen.
//
// The first narrow-screen version of this picker did exactly that. Anchoring
// the panel to its own summary put it off the right edge of a 320px viewport,
// and the obvious repair — putting it back in flow — traded an overflow for a
// header that grew. It is anchored to the header bar instead: out of flow, and
// inside the screen.
//
// Measured rather than read off the stylesheet, because "does this element
// participate in layout" is a question about the rendered box tree and a media
// query cannot be inspected for the answer.
func TestOpeningTheLanguagePickerDoesNotGrowTheHeader(t *testing.T) {
	chrome := findChrome(t)
	mux, _ := newTestMux(t, nil)
	srv := httptest.NewServer(langHarness(mux, "/records"))
	defer srv.Close()

	var reports []langReport
	payload := renderMeasurement(t, chrome, srv.URL+langMeasurePath)
	if err := json.Unmarshal([]byte(payload), &reports); err != nil {
		t.Fatalf("measurement %q: %v", payload, err)
	}
	if len(reports) != len(langViewports) {
		t.Fatalf("measured %d viewports, want %d", len(reports), len(langViewports))
	}

	for _, r := range reports {
		if !r.Found {
			t.Errorf("viewport %dpx: no language picker on the page", r.Width)
			continue
		}
		if r.OpenHeader != r.ClosedHeader {
			t.Errorf("viewport %dpx: header is %.0fpx closed and %.0fpx open — opening the picker resized it",
				r.Width, r.ClosedHeader, r.OpenHeader)
		}
		// Out of flow is the mechanism, and it is worth naming: a panel that
		// happens not to grow the header today because it is short would
		// start growing it the day a tenth language is added.
		if r.PanelPosition == "static" || r.PanelPosition == "relative" {
			t.Errorf("viewport %dpx: the panel is %s, so it takes part in the header's layout",
				r.Width, r.PanelPosition)
		}
		// And it still has to be on screen. The overflow this replaced was
		// real; trading it for a growing header would not have been a fix.
		if r.PanelRight > r.ClientWidth+0.5 {
			t.Errorf("viewport %dpx: the open panel reaches x=%.0f past clientWidth %.0f",
				r.Width, r.PanelRight, r.ClientWidth)
		}
		if r.PanelLeft < -0.5 {
			t.Errorf("viewport %dpx: the open panel starts at x=%.0f, off the left edge", r.Width, r.PanelLeft)
		}
		// A picker nobody can read is not a picker. Every locale has to be
		// visible in the open panel.
		if r.Options < 9 {
			t.Errorf("viewport %dpx: the open panel shows %d languages, want 9", r.Width, r.Options)
		}
	}
}

// langViewports covers the phone widths the narrow-screen rules apply at, plus
// one desktop width where the panel anchors to the summary instead — the two
// rules are different code paths and the header must not grow under either.
var langViewports = []int{320, 390, 900}

type langReport struct {
	Width         int     `json:"width"`
	Found         bool    `json:"found"`
	ClosedHeader  float64 `json:"closedHeader"`
	OpenHeader    float64 `json:"openHeader"`
	PanelPosition string  `json:"panelPosition"`
	PanelLeft     float64 `json:"panelLeft"`
	PanelRight    float64 `json:"panelRight"`
	ClientWidth   float64 `json:"clientWidth"`
	Options       int     `json:"options"`
}

const langMeasurePath = "/__language-picker-measure"

func langHarness(mux *http.ServeMux, target string) http.Handler {
	widths := make([]string, 0, len(langViewports))
	frames := make([]string, 0, len(langViewports))
	for _, w := range langViewports {
		widths = append(widths, fmt.Sprint(w))
		frames = append(frames, fmt.Sprintf(
			`<iframe id="l%d" width="%d" height="900" src="%s"></iframe>`, w, w, html.EscapeString(target)))
	}
	page := strings.NewReplacer(
		"__WIDTHS__", strings.Join(widths, ","),
		"__FRAMES__", strings.Join(frames, "\n"),
	).Replace(langHarnessHTML)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != langMeasurePath {
			mux.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
}

// langHarnessHTML measures the header before and after opening the picker.
//
// The header is read with getBoundingClientRect rather than offsetHeight so a
// fractional growth still shows: a panel that adds half a pixel is still a
// panel taking part in the layout, and rounding it away would hide exactly the
// case this test exists for.
const langHarnessHTML = `<!doctype html>
<meta charset="utf-8"><title>language picker measure</title>
<body style="margin:0">
__FRAMES__
<pre id="measure">PENDING</pre>
<script>
function headerHeight(doc){
  var top = doc.querySelector('header.top');
  return top ? top.getBoundingClientRect().height : -1;
}
function run(){
  var out = [];
  var widths = [__WIDTHS__];
  for (var i = 0; i < widths.length; i++) {
    var w = widths[i], fr = document.getElementById('l' + w);
    var doc = fr.contentDocument, win = fr.contentWindow;
    var pick = doc.querySelector('details.langpick');
    var rec = {width: w, found: !!pick, closedHeader: headerHeight(doc),
               openHeader: -1, panelPosition: '', panelLeft: 0, panelRight: 0,
               clientWidth: doc.documentElement.clientWidth, options: 0};
    if (pick) {
      pick.open = true;
      // Force layout before reading, so the open state is measured and not
      // the frame before it.
      void doc.documentElement.offsetHeight;
      rec.openHeader = headerHeight(doc);
      var ul = pick.querySelector('ul');
      if (ul) {
        var r = ul.getBoundingClientRect();
        rec.panelPosition = win.getComputedStyle(ul).position;
        rec.panelLeft = Math.round(r.left + win.scrollX);
        rec.panelRight = Math.round(r.right + win.scrollX);
        rec.options = ul.querySelectorAll('a').length;
      }
      pick.open = false;
    }
    out.push(rec);
  }
  document.getElementById('measure').textContent = JSON.stringify(out);
}
window.addEventListener('load', function(){ setTimeout(run, 400); });
</script>
`
