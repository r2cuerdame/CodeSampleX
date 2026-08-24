package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// The sample page overflowed horizontally on a phone and every assertion in
// this package said it was fine, because they all read the stylesheet as
// text. Overflow is not a string, it is geometry: a box whose right edge
// lands past the viewport. So this test renders the real page with the real
// stylesheet in a real browser and measures the document.
//
// Headless Chrome will not size its own window below ~500 CSS px, which is
// why an earlier measurement (docs/verifier-digest-production-evidence-2026-08-23.md
// §6) read 497 for a 360 request and concluded the narrow layout was clean.
// An iframe has no such floor: inside it, 100vw, the scrollbar and
// documentElement.scrollWidth all behave exactly as they do in a window that
// width. That is the difference between missing this bug and finding it.

// narrowViewports are the widths the page has to survive. 320 is the
// narrowest phone still in use, 360 the most common one, 480 the width where
// the bug reported in R2C-148 stopped reproducing.
var narrowViewports = []int{320, 360, 480}

type viewportBox struct {
	Sel   string  `json:"sel"`
	Left  float64 `json:"left"`
	Right float64 `json:"right"`
	Width float64 `json:"width"`
	Text  string  `json:"text"`
}

type viewportState struct {
	ScrollWidth float64       `json:"scrollWidth"`
	ClientWidth float64       `json:"clientWidth"`
	Offenders   []viewportBox `json:"offenders"`
	Tips        []viewportBox `json:"tips"`
}

type viewportReport struct {
	Width  int           `json:"width"`
	Closed viewportState `json:"closed"`
	Open   viewportState `json:"open"`
}

func (s viewportState) overflow() float64 { return s.ScrollWidth - s.ClientWidth }

func TestSampleDetailFitsNarrowViewports(t *testing.T) {
	chrome := findChrome(t)

	// The detail page is where the bug was reported. The symbol page carries
	// the same tooltip from the other direction — anchored to a list row
	// rather than to the hero's badge strip — so a fix that only holds for
	// one of the two anchors is not a fix.
	for _, page := range []struct{ name, path string }{
		{"sample detail", "/samples/sha256:d1e2f3"},
		{"symbol sample list", "/npm/axios/1.12.0/axios.post"},
	} {
		t.Run(page.name, func(t *testing.T) {
			measurePage(t, chrome, page.path)
		})
	}
}

func measurePage(t *testing.T, chrome, path string) {
	t.Helper()

	// The deployed footer prints the release commit — 40 unbroken hex
	// characters. The old fixture version ("1.0.0-test") is short enough to
	// fit anywhere, so it hid a line that overflows in production.
	mux, _ := newTestMux(t, func(d *Deps) {
		d.Version = "3ca13b91c900cb721572f35bdae81dcc3c61e433"
	})
	srv := httptest.NewServer(measureHarness(mux, path))
	defer srv.Close()

	reports := measureViewports(t, chrome, srv.URL+measurePath)

	for _, r := range reports {
		for _, st := range []struct {
			name  string
			state viewportState
		}{{"tooltip closed", r.Closed}, {"tooltip open", r.Open}} {
			if got := st.state.overflow(); got > 0 {
				t.Errorf("viewport %dpx, %s: documentElement.scrollWidth %.0f > clientWidth %.0f (%.0fpx of horizontal overflow)",
					r.Width, st.name, st.state.ScrollWidth, st.state.ClientWidth, got)
			}
			for _, o := range st.state.Offenders {
				t.Errorf("viewport %dpx, %s: %s reaches x=%.0f past clientWidth %.0f (left=%.0f width=%.0f) %q",
					r.Width, st.name, o.Sel, o.Right, st.state.ClientWidth, o.Left, o.Width, o.Text)
			}
		}

		// An open tooltip must still be readable. Clamping it to something
		// that cannot overflow is only a fix if the text it holds is one a
		// person can read at that width.
		if len(r.Open.Tips) == 0 {
			t.Errorf("viewport %dpx: no tooltip was measured open", r.Width)
		}
		for _, tip := range r.Open.Tips {
			if tip.Left < 0 {
				t.Errorf("viewport %dpx: tooltip %s starts off-screen at x=%.0f", r.Width, tip.Sel, tip.Left)
			}
			if tip.Right > r.Open.ClientWidth {
				t.Errorf("viewport %dpx: tooltip %s ends at x=%.0f, past clientWidth %.0f",
					r.Width, tip.Sel, tip.Right, r.Open.ClientWidth)
			}
			// Below this the tooltip has been "fixed" by making it too
			// narrow to read rather than by placing it correctly.
			if min := 0.6 * r.Open.ClientWidth; tip.Width < min {
				t.Errorf("viewport %dpx: tooltip %s is %.0fpx wide, under %.0fpx — too narrow to read",
					r.Width, tip.Sel, tip.Width, min)
			}
		}
	}
}

// TestNarrowLayoutRulesSurviveWithoutABrowser keeps the three fixes legible
// to a reader of the stylesheet, and keeps them asserted on a machine with no
// Chrome. It is a weaker check than the render above, not a substitute: it
// says the rules are present, not that the page fits.
func TestNarrowLayoutRulesSurviveWithoutABrowser(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	css := get(t, mux, "/static/site.css")
	if css.Code != http.StatusOK {
		t.Fatalf("css status %d", css.Code)
	}
	sheet := css.Body.String()

	for _, want := range []string{
		// A tooltip is sized against the row it hangs off, never against
		// 100vw — 100vw counts the scrollbar the content cannot use.
		".badge-tip {",
		"width: min(22rem, 100%);",
		// ...and that row is what positions it, so its left edge is the
		// row's left edge rather than an arbitrary offset mid-line.
		".badges, .samples li { position: relative; }",
		".badges .badge-help, .samples .badge-help { position: static; }",
		// The body floor may not exceed the width the viewport actually
		// offers, which is a scrollbar narrower than 100vw.
		"min-width: min(20rem, 100%);",
		// The release commit in the footer is 40 unbroken characters.
		".foot .mono { overflow-wrap: anywhere; }",
	} {
		mustContain(t, sheet, want)
	}
}

const measurePath = "/__narrow-viewport-measure"

// measureHarness serves the site under test plus one extra page that loads a
// path of it in a fixed-width iframe and reports the geometry.
func measureHarness(mux *http.ServeMux, target string) http.Handler {
	widths := make([]string, 0, len(narrowViewports))
	frames := make([]string, 0, len(narrowViewports))
	for _, w := range narrowViewports {
		widths = append(widths, fmt.Sprint(w))
		frames = append(frames, fmt.Sprintf(
			`<iframe id="f%d" width="%d" height="900" src="%s"></iframe>`, w, w, html.EscapeString(target)))
	}
	page := strings.NewReplacer(
		"__WIDTHS__", strings.Join(widths, ","),
		"__FRAMES__", strings.Join(frames, "\n"),
	).Replace(measureHarnessHTML)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != measurePath {
			mux.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
}

// measureHarnessHTML reports, per viewport and for both tooltip states, the
// document width and every box that reaches past it. An element only counts
// as an offender if nothing between it and the document clips overflow: the
// runs table is meant to scroll inside .tablewrap, and that is not a page
// overflow. body is skipped as a clipper on purpose — its overflow-x
// propagates to the viewport, so it hides the scrollbar without shrinking
// scrollWidth, which is exactly how this bug stayed invisible.
const measureHarnessHTML = `<!doctype html>
<meta charset="utf-8"><title>narrow viewport measure</title>
<body style="margin:0">
__FRAMES__
<pre id="measure">PENDING</pre>
<script>
function label(el){
  var c = typeof el.className === 'string' ? el.className.trim().replace(/\s+/g,'.') : '';
  return el.tagName.toLowerCase() + (c ? '.' + c : '');
}
function box(el, win){
  var r = el.getBoundingClientRect();
  return {sel: label(el), left: Math.round(r.left + win.scrollX), right: Math.round(r.right + win.scrollX),
          width: Math.round(r.width), text: (el.textContent || '').trim().slice(0, 60)};
}
function clipped(el, doc, win){
  for (var p = el.parentElement; p && p !== doc.documentElement && p !== doc.body; p = p.parentElement) {
    if (win.getComputedStyle(p).overflowX !== 'visible') return true;
  }
  return false;
}
function state(doc, win){
  var de = doc.documentElement, cw = de.clientWidth, offenders = [];
  var all = doc.querySelectorAll('body *');
  for (var i = 0; i < all.length; i++) {
    var el = all[i], r = el.getBoundingClientRect();
    if (!r.width && !r.height) continue;
    if (r.right + win.scrollX > cw + 0.5 && !clipped(el, doc, win)) offenders.push(box(el, win));
  }
  offenders.sort(function(a, b){ return b.right - a.right; });
  var tips = [], t = doc.querySelectorAll('.badge-help.open .badge-tip');
  for (var j = 0; j < t.length; j++) tips.push(box(t[j], win));
  return {scrollWidth: de.scrollWidth, clientWidth: cw, offenders: offenders.slice(0, 12), tips: tips};
}
function run(){
  var out = [];
  var widths = [__WIDTHS__];
  for (var i = 0; i < widths.length; i++) {
    var w = widths[i], fr = document.getElementById('f' + w);
    var doc = fr.contentDocument, win = fr.contentWindow;
    var closed = state(doc, win);
    var helps = doc.querySelectorAll('.badge-help');
    for (var j = 0; j < helps.length; j++) helps[j].classList.add('open');
    var open = state(doc, win);
    for (var k = 0; k < helps.length; k++) helps[k].classList.remove('open');
    out.push({width: w, closed: closed, open: open});
  }
  document.getElementById('measure').textContent = JSON.stringify(out);
}
window.addEventListener('load', function(){ setTimeout(run, 400); });
</script>
`

var measureRe = regexp.MustCompile(`(?s)<pre id="measure">(.*?)</pre>`)

func measureViewports(t *testing.T, chrome, url string) []viewportReport {
	t.Helper()
	profile := t.TempDir()
	cmd := exec.Command(chrome,
		"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--hide-scrollbars=false",
		// The page under test is this process's own httptest server on
		// loopback, and the profile is thrown away. These two keep CI
		// containers (no user namespaces, a small /dev/shm) from turning a
		// layout check into a flake.
		"--no-sandbox", "--disable-dev-shm-usage",
		"--user-data-dir="+filepath.Join(profile, "chrome"),
		"--virtual-time-budget=8000", "--dump-dom", url)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("headless chrome: %v", err)
	}
	m := measureRe.FindSubmatch(out)
	if m == nil {
		t.Fatalf("no measurement in rendered DOM (%d bytes)", len(out))
	}
	payload := html.UnescapeString(string(m[1]))
	if strings.TrimSpace(payload) == "PENDING" {
		t.Fatal("the page never reported a measurement")
	}
	var reports []viewportReport
	if err := json.Unmarshal([]byte(payload), &reports); err != nil {
		t.Fatalf("measurement %q: %v", payload, err)
	}
	if len(reports) != len(narrowViewports) {
		t.Fatalf("measured %d viewports, want %d", len(reports), len(narrowViewports))
	}
	return reports
}

// findChrome locates a Chrome to measure in. A skip here is "not measured on
// this machine", not "passed" — CSX_CHROME names the binary outright, and
// CSX_REQUIRE_CHROME=1 turns a missing one into a failure that says so, the
// way CSX_REQUIRE_TEST_DSN does for the database suite. CI sets it: a layout
// nobody measured is exactly how the bug behind R2C-148 reached production.
func findChrome(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("CSX_CHROME"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
			if base != "" {
				candidates = append(candidates, filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
	case "darwin":
		candidates = append(candidates, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if os.Getenv("CSX_REQUIRE_CHROME") == "1" {
		t.Fatal("CSX_REQUIRE_CHROME=1 but no Chrome was found: set CSX_CHROME to the binary")
	}
	t.Skip("no Chrome found: this viewport was not measured (set CSX_CHROME)")
	return ""
}
