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

// The dependency-health card quotes the fingerprint of the first observed
// failure whole: "sha256:" and 64 hex digits, 71 characters with no break
// opportunity in them, in a monospace face. On an iPhone that one <code> is
// wider than the screen, and nothing gave it a wrap rule, so it spilled out
// of the card's evidence line and the page grew to hold it (#228). body
// carries overflow-x:hidden, which hides the scrollbar without shrinking
// scrollWidth — a desktop review saw nothing.
//
// The fix has to be local. The dependency tables beside the card are meant
// to scroll sideways inside .tablewrap, and a global word-break would tear
// version numbers and library names apart in every table on the site. So
// this measures three things in a real browser, on the real page with the
// real stylesheet: the document fits, the whole fingerprint is still there
// and wraps onto more than one line, and the table next to it still scrolls
// inside its own wrapper.

// dephealthViewports: 320 is the narrowest phone still in use, 360 the most
// common one, 390 and 430 the two iPhone classes the issue names, and 1280
// the desktop smoke — the line must not regress to wrapping where it fits.
var dephealthViewports = []int{320, 360, 390, 430, 1280}

// dephealthFingerprint is the production shape: sha256 and a full digest.
// The other package fixtures use short names like ERR_REQUIRE_ESM, which fit
// anywhere and would have hidden this.
const dephealthFingerprint = "sha256:" +
	"9c1f7a3e5b2d8046e7a4c0b95d31f862" +
	"4b8e6d20fa937c150d5837e1b6ac492f"

type dephealthBox struct {
	Sel   string  `json:"sel"`
	Left  float64 `json:"left"`
	Right float64 `json:"right"`
	Width float64 `json:"width"`
	// Host is the content width of the parent: a card wider than its section
	// is a card that pushed its container open.
	Host float64 `json:"host"`
	// Lines is how many line boxes an inline element occupies — the
	// fingerprint wrapping is Lines > 1, not a style read as text.
	Lines int `json:"lines"`
	// OverflowX is the computed overflow-x, reported for the table wrappers.
	OverflowX string `json:"overflowX"`
	FullToken bool   `json:"fullToken"`
	Text      string `json:"text"`
}

type dephealthState struct {
	ScrollWidth float64        `json:"scrollWidth"`
	ClientWidth float64        `json:"clientWidth"`
	Offenders   []dephealthBox `json:"offenders"`
	Card        []dephealthBox `json:"card"`
	Fingerprint []dephealthBox `json:"fingerprint"`
	Tables      []dephealthBox `json:"tables"`
}

type dephealthReport struct {
	Width int            `json:"width"`
	State dephealthState `json:"state"`
}

// seedDephealthBreak gives /npm/axios a pinned release whose first observed
// failure carries a full sha256 fingerprint, a same-receipt dependency
// failure beside it, and enough dependencies for the tables to render.
func seedDephealthBreak(f *fakeStore) {
	f.dependencies = []DependencyEdge{
		{ParentVersion: "2.0.0", ChildName: "alpha", ChildVersion: "2.0.0", SameReceipt: true, Outcome: "fail", Projects: 6},
		{ParentVersion: "1.0.0", ChildName: "alpha", ChildVersion: "1.0.0", Projects: 4},
		{ParentVersion: "2.0.0", ChildName: "beta", ChildVersion: "1.0.0", Projects: 6},
		{ParentVersion: "1.0.0", ChildName: "beta", ChildVersion: "1.0.0", Projects: 4},
		{ParentVersion: "2.0.0", ChildName: "gamma-long-library-name", ChildVersion: "1.0.0", Projects: 6},
		{ParentVersion: "1.0.0", ChildName: "gamma-long-library-name", ChildVersion: "1.0.0", Projects: 4},
	}
	f.clusters["npm|axios"] = []string{
		`{"stage":"PROJECT_TEST","fingerprint":"` + dephealthFingerprint + `","count":9,"observationCount":12,` +
			`"versions":["2.0.0"],"envSummary":{"os":"linux","runtime":"node@22"}}`,
	}
}

func TestDependencyHealthFingerprintFitsNarrowViewports(t *testing.T) {
	chrome := findChrome(t)

	mux, store := newTestMux(t, func(d *Deps) {
		// The deployed footer prints the release commit — 40 unbroken hex
		// characters, which the short fixture version would have hidden.
		d.Build.Revision = "3ca13b91c900cb721572f35bdae81dcc3c61e433"
	})
	seedDephealthBreak(store)
	path := "/npm/axios?f_version=2.0.0"

	// The fixture has to reach the page before geometry means anything.
	body := mustGet(t, mux, path)
	mustContain(t, body, `class="dephealth-break"`)
	mustContain(t, body, dephealthFingerprint)

	srv := httptest.NewServer(dephealthHarness(mux, path))
	defer srv.Close()

	for _, r := range measureDephealth(t, chrome, srv.URL+dephealthMeasurePath) {
		st := r.State
		mobile := r.Width <= 430

		// 1. No horizontal page overflow, read as a number rather than as a
		// scrollbar — body hides the scrollbar.
		if over := st.ScrollWidth - st.ClientWidth; over > 0 {
			t.Errorf("viewport %dpx: documentElement.scrollWidth %.0f > clientWidth %.0f (%.0fpx of horizontal overflow)",
				r.Width, st.ScrollWidth, st.ClientWidth, over)
		}
		for _, o := range st.Offenders {
			t.Errorf("viewport %dpx: %s reaches x=%.0f past clientWidth %.0f (left=%.0f width=%.0f) %q",
				r.Width, o.Sel, o.Right, st.ClientWidth, o.Left, o.Width, o.Text)
		}

		// 2. The card stays shrinkable: inside the viewport and no wider
		// than the section holding it.
		if len(st.Card) == 0 {
			t.Fatalf("viewport %dpx: no .dephealth-summary was measured — the fixture never reached the page", r.Width)
		}
		for _, c := range st.Card {
			if c.Right > st.ClientWidth+0.5 {
				t.Errorf("viewport %dpx: card %s ends at x=%.0f, past clientWidth %.0f", r.Width, c.Sel, c.Right, st.ClientWidth)
			}
			if c.Host > 0 && c.Width > c.Host+0.5 {
				t.Errorf("viewport %dpx: card %s is %.0fpx wide inside a %.0fpx section — its contents widened it",
					r.Width, c.Sel, c.Width, c.Host)
			}
		}

		// 3. The fingerprint is complete and, on a phone, wraps across
		// lines. Clipping it or shortening it would satisfy the geometry
		// above and destroy the evidence, which the issue rules out.
		if len(st.Fingerprint) == 0 {
			t.Fatalf("viewport %dpx: the fingerprint <code> never rendered", r.Width)
		}
		for _, fp := range st.Fingerprint {
			t.Logf("viewport %dpx: %s spans %d line(s), left=%.0f right=%.0f of clientWidth %.0f",
				r.Width, fp.Sel, fp.Lines, fp.Left, fp.Right, st.ClientWidth)
			if !fp.FullToken {
				t.Errorf("viewport %dpx: %s no longer carries the whole fingerprint — it was truncated, not wrapped", r.Width, fp.Sel)
			}
			if fp.Right > st.ClientWidth+0.5 {
				t.Errorf("viewport %dpx: %s ends at x=%.0f, past clientWidth %.0f", r.Width, fp.Sel, fp.Right, st.ClientWidth)
			}
			if mobile && fp.Lines < 2 {
				t.Errorf("viewport %dpx: %s occupies %d line box(es); a 71-character value narrower than the screen was hidden, not wrapped",
					r.Width, fp.Sel, fp.Lines)
			}
			if !mobile && fp.Lines != 1 {
				t.Errorf("viewport %dpx: %s wraps onto %d lines on a desktop where it fits", r.Width, fp.Sel, fp.Lines)
			}
		}

		// 4. The dependency tables beside the card still scroll locally.
		// The fix must not have removed their wrapper or made the wrapper
		// visible-overflow; if the table is wider than the phone it scrolls
		// inside .tablewrap rather than through the page.
		if len(st.Tables) == 0 {
			t.Fatalf("viewport %dpx: no .tablewrap was measured next to the card", r.Width)
		}
		for _, tw := range st.Tables {
			if tw.OverflowX != "auto" && tw.OverflowX != "scroll" {
				t.Errorf("viewport %dpx: %s has overflow-x %q; the table lost its local scroll", r.Width, tw.Sel, tw.OverflowX)
			}
			if tw.Right > st.ClientWidth+0.5 {
				t.Errorf("viewport %dpx: %s ends at x=%.0f, past clientWidth %.0f", r.Width, tw.Sel, tw.Right, st.ClientWidth)
			}
		}
	}
}

// TestDependencyHealthWrapRulesSurviveWithoutABrowser keeps the fix legible
// to a reader of the stylesheet and asserted on a machine with no Chrome. It
// says the rules are present, not that the page fits — the render above is
// the test; this is what a skipped render leaves behind.
func TestDependencyHealthWrapRulesSurviveWithoutABrowser(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	css := get(t, mux, "/static/site.css")
	if css.Code != http.StatusOK {
		t.Fatalf("css status %d", css.Code)
	}
	sheet := css.Body.String()
	for _, want := range []string{
		// The card and its break block may shrink below their content.
		".dephealth-summary, .dephealth-break { min-width: 0; }",
		// The fingerprint wraps inside its own line; nothing else on the
		// site takes this rule.
		".dephealth-break .break-evidence code { overflow-wrap: anywhere; }",
	} {
		mustContain(t, sheet, want)
	}
	// The scoped rule is the whole fix. A global one would tear version
	// numbers apart in every table.
	for _, forbid := range []string{
		"code { overflow-wrap: anywhere; }",
		"* { overflow-wrap: anywhere; }",
		"* { word-break: break-all; }",
	} {
		if strings.Contains(sheet, "\n"+forbid) {
			t.Errorf("stylesheet carries a global wrapping rule %q", forbid)
		}
	}
}

const dephealthMeasurePath = "/__dephealth-overflow-measure"

// dephealthHarness serves the site under test plus one page that loads
// target in a fixed-width iframe per viewport and reports the geometry. The
// iframe is what makes phone widths measurable: a headless Chrome window will
// not size itself below ~500 CSS px, and an iframe has no floor.
func dephealthHarness(mux *http.ServeMux, target string) http.Handler {
	widths := make([]string, 0, len(dephealthViewports))
	frames := make([]string, 0, len(dephealthViewports))
	for _, w := range dephealthViewports {
		widths = append(widths, fmt.Sprint(w))
		frames = append(frames, fmt.Sprintf(
			`<iframe id="d%d" width="%d" height="1400" src="%s"></iframe>`, w, w, html.EscapeString(target)))
	}
	page := strings.NewReplacer(
		"__WIDTHS__", strings.Join(widths, ","),
		"__FRAMES__", strings.Join(frames, "\n"),
		"__TOKEN__", dephealthFingerprint,
	).Replace(dephealthHarnessHTML)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != dephealthMeasurePath {
			mux.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
}

// dephealthHarnessHTML reports, per viewport: the document width, every
// unclipped box past it, the card, the fingerprint <code> and every
// .tablewrap in the dependency-health section.
//
// An element only counts as an offender if nothing between it and the
// document clips overflow — a table scrolling inside .tablewrap is not a page
// overflow. body is skipped as a clipper on purpose: its overflow-x:hidden
// propagates to the viewport and removes the scrollbar without removing the
// overflow.
const dephealthHarnessHTML = `<!doctype html>
<meta charset="utf-8"><title>dependency health overflow measure</title>
<body style="margin:0">
__FRAMES__
<pre id="measure">PENDING</pre>
<script>
var TOKEN = "__TOKEN__";
function label(el){
  var c = typeof el.className === 'string' ? el.className.trim().replace(/\s+/g,'.') : '';
  return el.tagName.toLowerCase() + (c ? '.' + c : '');
}
function box(el, win){
  var r = el.getBoundingClientRect(), text = (el.textContent || '').trim();
  var host = el.parentElement ? el.parentElement.clientWidth : 0;
  /* An inline box has no clientWidth; its line fragments are the truth, and
     their count is how many lines it spans. A block has exactly one. */
  return {sel: label(el), left: Math.round(r.left + win.scrollX), right: Math.round(r.right + win.scrollX),
          width: Math.round(r.width), host: host,
          lines: el.getClientRects().length, overflowX: win.getComputedStyle(el).overflowX,
          fullToken: text.indexOf(TOKEN) !== -1, text: text.slice(0, 60)};
}
function clipped(el, doc, win){
  for (var p = el.parentElement; p && p !== doc.documentElement && p !== doc.body; p = p.parentElement) {
    if (win.getComputedStyle(p).overflowX !== 'visible') return true;
  }
  return false;
}
function boxes(doc, win, sel){
  var out = [], found = doc.querySelectorAll(sel);
  for (var k = 0; k < found.length; k++) out.push(box(found[k], win));
  return out;
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
  return {scrollWidth: de.scrollWidth, clientWidth: cw, offenders: offenders.slice(0, 12),
          card: boxes(doc, win, '#dependency-health .dephealth-summary'),
          fingerprint: boxes(doc, win, '#dependency-health .break-evidence code'),
          tables: boxes(doc, win, '#dependency-health .tablewrap')};
}
function run(){
  var out = [], widths = [__WIDTHS__];
  for (var i = 0; i < widths.length; i++) {
    var w = widths[i], fr = document.getElementById('d' + w);
    out.push({width: w, state: state(fr.contentDocument, fr.contentWindow)});
  }
  document.getElementById('measure').textContent = JSON.stringify(out);
}
window.addEventListener('load', function(){ setTimeout(run, 400); });
</script>
`

func measureDephealth(t *testing.T, chrome, url string) []dephealthReport {
	t.Helper()
	payload := renderMeasurement(t, chrome, url)
	var reports []dephealthReport
	if err := json.Unmarshal([]byte(payload), &reports); err != nil {
		t.Fatalf("measurement %q: %v", payload, err)
	}
	if len(reports) != len(dephealthViewports) {
		t.Fatalf("measured %d viewports, want %d", len(reports), len(dephealthViewports))
	}
	return reports
}
