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

// The home page must not open holes between its sections.
//
// It carried five sections and now carries three. `section { margin-bottom:
// 4.5rem }` was chosen for the dense page and, with the strips gone, reads as
// dead space: 72px of nothing between the hero and the first card row, and
// more than that again between the cards and the grid, because an
// .evidence-section adds its own padding on top of the margin.
//
// Measured rather than reasoned about. The gap a reader sees is the distance
// between one section's painted bottom edge and the next one's first text,
// which no stylesheet states in one place — it is margin plus padding plus
// whatever the first child brings.
func TestTheHomePageHasNoDeadSpaceBetweenSections(t *testing.T) {
	chrome := findChrome(t)
	mux, _ := newTestMux(t, nil)
	srv := httptest.NewServer(landingGapHarness(mux, "/ko/"))
	defer srv.Close()

	var reports []landingGapReport
	payload := renderMeasurement(t, chrome, srv.URL+landingGapMeasurePath)
	if err := json.Unmarshal([]byte(payload), &reports); err != nil {
		t.Fatalf("measurement %q: %v", payload, err)
	}
	for _, r := range reports {
		t.Logf("viewport %dpx gaps: %s", r.Width, r.All)
		if r.Sections < 3 {
			t.Fatalf("viewport %dpx: only %d sections measured", r.Width, r.Sections)
		}
		if r.MaxGap < 0 {
			t.Fatalf("viewport %dpx: no adjacent section pair was measured", r.Width)
		}
		// A section break should read as a break, not as a missing section.
		// 72px was the old rhythm; anything at or above it is what the
		// capture called out.
		if r.MaxGap >= 72 {
			t.Errorf("viewport %dpx: %.0fpx of empty space between %s — dead space, not a section break",
				r.Width, r.MaxGap, r.MaxGapBetween)
		}
		// And not so tight that two sections read as one.
		if r.MinGap < 16 {
			t.Errorf("viewport %dpx: only %.0fpx between %s — the sections run together",
				r.Width, r.MinGap, r.MinGapBetween)
		}
	}
}

var landingGapViewports = []int{390, 1280}

type landingGapReport struct {
	Width         int     `json:"width"`
	Sections      int     `json:"sections"`
	MaxGap        float64 `json:"maxGap"`
	MaxGapBetween string  `json:"maxGapBetween"`
	MinGap        float64 `json:"minGap"`
	MinGapBetween string  `json:"minGapBetween"`
	All           string  `json:"all"`
}

const landingGapMeasurePath = "/__landing-gap-measure"

func landingGapHarness(mux *http.ServeMux, target string) http.Handler {
	widths := make([]string, 0, len(landingGapViewports))
	frames := make([]string, 0, len(landingGapViewports))
	for _, w := range landingGapViewports {
		widths = append(widths, fmt.Sprint(w))
		frames = append(frames, fmt.Sprintf(
			`<iframe id="g%d" width="%d" height="2400" src="%s"></iframe>`, w, w, html.EscapeString(target)))
	}
	page := strings.NewReplacer(
		"__WIDTHS__", strings.Join(widths, ","),
		"__FRAMES__", strings.Join(frames, "\n"),
	).Replace(landingGapHarnessHTML)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != landingGapMeasurePath {
			mux.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
}

const landingGapHarnessHTML = `<!doctype html>
<meta charset="utf-8"><title>landing gap measure</title>
<body style="margin:0">
__FRAMES__
<pre id="measure">PENDING</pre>
<script>
// The gap a reader sees: from one section's painted bottom to the top of the
// next section's first piece of content, not to the section box itself --
// an .evidence-section pads its own top, and that padding is part of the hole.
function firstContentTop(sec){
  var best = null;
  var nodes = sec.querySelectorAll('h1,h2,h3,p,ul,ol,table,form,div.section-heading');
  for (var i = 0; i < nodes.length; i++) {
    var r = nodes[i].getBoundingClientRect();
    if (r.height > 0 && (best === null || r.top < best)) best = r.top;
  }
  return best === null ? sec.getBoundingClientRect().top : best;
}
function run(){
  var out = [];
  var widths = [__WIDTHS__];
  for (var i = 0; i < widths.length; i++) {
    var w = widths[i], doc = document.getElementById('g' + w).contentDocument;
    // Only ADJACENT sections. An element between two sections -- the ad slot
    // sits between the grid and the install block -- is a slot that fills in
    // production and is empty here, so the distance across it is not a gap
    // this page opened.
    var secs = doc.querySelectorAll('main > section');
    var adjacent = function(k){ return secs[k].nextElementSibling === secs[k+1]; };
    var maxGap = -1, minGap = 1e9, maxPair = '', minPair = '';
    for (var j = 0; j + 1 < secs.length; j++) {
      if (!adjacent(j)) continue;
      var bottom = secs[j].getBoundingClientRect().bottom;
      var gap = firstContentTop(secs[j+1]) - bottom;
      var pair = (secs[j].id || secs[j].className.split(' ')[0]) + ' → ' +
                 (secs[j+1].id || secs[j+1].className.split(' ')[0]);
      if (gap > maxGap) { maxGap = gap; maxPair = pair; }
      if (gap < minGap) { minGap = gap; minPair = pair; }
    }
    var all = [];
    for (var k = 0; k + 1 < secs.length; k++) {
      if (!adjacent(k)) continue;
      var b2 = secs[k].getBoundingClientRect().bottom;
      all.push(((secs[k].id || secs[k].className.split(' ')[0]) + '→' +
                (secs[k+1].id || secs[k+1].className.split(' ')[0]) + '=' +
                Math.round(firstContentTop(secs[k+1]) - b2)));
    }
    out.push({width: w, sections: secs.length, maxGap: maxGap,
              maxGapBetween: maxPair, minGap: minGap, minGapBetween: minPair,
              all: all.join('  ')});
  }
  document.getElementById('measure').textContent = JSON.stringify(out);
}
window.addEventListener('load', function(){ setTimeout(run, 400); });
</script>
`
