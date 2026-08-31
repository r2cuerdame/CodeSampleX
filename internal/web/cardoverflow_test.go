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

// A card is a box with a border around it, and a reader on a phone reads the
// border as the edge of the thing. R2C-196 reported the opposite: on
// codesamplex.dev the session-shaped string at the bottom of a card pushed
// the card itself past the viewport, so the box the reader was told to trust
// no longer fit the screen.
//
// R2C-148 was a different bug on the same page family — a hidden tooltip with
// a 352px fixed width — and is covered by narrowviewport_test.go. This one is
// about VISIBLE text: an unbroken token has a min-content width equal to its
// own length, and a flex or grid child whose min-width is auto hands that
// length straight to its parent. The card grows, and every ancestor with it.
//
// So the measurement here is deliberately geometric, not textual: render the
// real page with the real stylesheet and ask where the boxes actually landed.
// Reading the stylesheet for a rule cannot see a card widened by its content.

// cardViewports are the phone widths R2C-196 asks for. 320 is the narrowest
// phone still in use, 360 the most common, 390 and 430 the two current
// iPhone classes — the widths at which the reporter saw it.
var cardViewports = []int{320, 360, 390, 430}

// longSessionToken is 96 characters with no break opportunity in them: no
// space, no hyphen, no dot. A UUID would be weaker as a fixture because CSS
// may break after each hyphen; a session id or a hash may not be broken
// anywhere at all, and that is the case that has to hold.
const longSessionToken = "9c1f7a3e5b2d8046" +
	"e7a4c0b95d31f862" +
	"4b8e6d20fa937c15" +
	"0d5837e1b6ac492f" +
	"a2f94c60e3b17d58" +
	"6e0b23d84f75a91c"

type cardBox struct {
	Sel string `json:"sel"`
	// Left and Right are document coordinates: past ClientWidth is off-screen.
	Left  float64 `json:"left"`
	Right float64 `json:"right"`
	Width float64 `json:"width"`
	// Inner is the element's own content box, Content what it wants to be.
	// Content > Inner means the text is spilling out of this element rather
	// than wrapping inside it.
	Inner   float64 `json:"inner"`
	Content float64 `json:"content"`
	// Host is the content width of the element's parent. A card wider than
	// its own list is a card that pushed its container open.
	Host      float64 `json:"host"`
	FullToken bool    `json:"fullToken"`
	Text      string  `json:"text"`
}

type cardState struct {
	ScrollWidth float64   `json:"scrollWidth"`
	ClientWidth float64   `json:"clientWidth"`
	Offenders   []cardBox `json:"offenders"`
	Cards       []cardBox `json:"cards"`
	Tokens      []cardBox `json:"tokens"`
}

type cardReport struct {
	Width int       `json:"width"`
	State cardState `json:"state"`
}

// TestCardsFitNarrowViewports is the R2C-196 regression. Every assertion in
// the issue's list is one of the four checks below, measured on the real
// pages that carry cards.
func TestCardsFitNarrowViewports(t *testing.T) {
	chrome := findChrome(t)

	for _, page := range []struct {
		name    string
		path    string
		cardSel string
		// wantToken is true when the fixture put longSessionToken on this
		// page, so the token's own wrapping can be asserted too.
		wantToken bool
		seed      func(*fakeStore)
	}{
		// The reported page. The bottom line of a finding card is prose that
		// quotes what the contract measured, and a measurement quotes
		// identifiers: Plug.Session.init/1, CanvasRenderingContext2D.getImageData,
		// a session id. The fixture is the worst of those shapes.
		{"findings", "/findings", "li.finding", true, seedSessionFinding},
		// The same card, three of them, in the landing page's grid. Its
		// track floor is its own failure mode, so it has to be measured
		// where it is laid out rather than inferred from the other page.
		{"landing", "/", "li.finding-card", false, nil},
		// Not a card, the same defect: a Go pseudo-version is 36 unbroken
		// characters inside a pill that may not wrap.
		{"records", "/compatibility", ".pkglist li", true, seedSessionRecord},
	} {
		t.Run(page.name, func(t *testing.T) {
			mux, store := newTestMux(t, func(d *Deps) {
				d.Build.Revision = "3ca13b91c900cb721572f35bdae81dcc3c61e433"
			})
			if page.seed != nil {
				page.seed(store)
			}
			// Derived findings intentionally fill out of band now. Make the
			// browser measure the seeded card after that cache is complete;
			// every geometric and full-token assertion below remains unchanged.
			if page.name == "findings" {
				getEventually(t, mux, page.path, longSessionToken)
			}
			srv := httptest.NewServer(cardHarness(mux, page.path, page.cardSel))
			defer srv.Close()

			for _, r := range measureCards(t, chrome, srv.URL+cardMeasurePath) {
				st := r.State

				// 1. No body-level horizontal scroll. body carries
				// overflow-x:hidden, which hides the scrollbar without
				// shrinking scrollWidth — so this reads the number, not the
				// scrollbar, exactly as the issue asks.
				if over := st.ScrollWidth - st.ClientWidth; over > 0 {
					t.Errorf("viewport %dpx: documentElement.scrollWidth %.0f > clientWidth %.0f (%.0fpx of horizontal overflow)",
						r.Width, st.ScrollWidth, st.ClientWidth, over)
				}
				for _, o := range st.Offenders {
					t.Errorf("viewport %dpx: %s reaches x=%.0f past clientWidth %.0f (left=%.0f width=%.0f) %q",
						r.Width, o.Sel, o.Right, st.ClientWidth, o.Left, o.Width, o.Text)
				}

				// 2. The card itself stays inside the viewport, both edges.
				if len(st.Cards) == 0 {
					t.Fatalf("viewport %dpx: no %s was measured — the fixture never reached the page", r.Width, page.cardSel)
				}
				for _, c := range st.Cards {
					if c.Right > st.ClientWidth+0.5 {
						t.Errorf("viewport %dpx: card %s ends at x=%.0f, past clientWidth %.0f",
							r.Width, c.Sel, c.Right, st.ClientWidth)
					}
					if c.Left < -0.5 {
						t.Errorf("viewport %dpx: card %s starts off-screen at x=%.0f", r.Width, c.Sel, c.Left)
					}
					// 3. The card was not widened by what is inside it. A
					// card wider than the list holding it is a card that
					// pushed its own container open — which is precisely how
					// one long token moves the whole page.
					if c.Host > 0 && c.Width > c.Host+0.5 {
						t.Errorf("viewport %dpx: card %s is %.0fpx wide inside a %.0fpx list — its contents widened it",
							r.Width, c.Sel, c.Width, c.Host)
					}
				}

				if !page.wantToken {
					continue
				}
				// 4. The long token is shown, and shown readably: it wraps
				// inside its own box rather than spilling out of it, and the
				// whole value is still in the document. Clipping the string
				// with overflow:hidden would satisfy the geometry above and
				// destroy the information, which the issue rules out.
				if len(st.Tokens) == 0 {
					t.Fatalf("viewport %dpx: the %d-character token never rendered", r.Width, len(longSessionToken))
				}
				for _, tok := range st.Tokens {
					if !tok.FullToken {
						t.Errorf("viewport %dpx: %s no longer carries the whole token — it was truncated away, not wrapped",
							r.Width, tok.Sel)
					}
					if tok.Content > tok.Inner+1 {
						t.Errorf("viewport %dpx: %s wants %.0fpx inside a %.0fpx box — the token is not wrapping",
							r.Width, tok.Sel, tok.Content, tok.Inner)
					}
					if tok.Right > st.ClientWidth+0.5 {
						t.Errorf("viewport %dpx: %s ends at x=%.0f, past clientWidth %.0f",
							r.Width, tok.Sel, tok.Right, st.ClientWidth)
					}
				}
			}
		})
	}
}

// seedSessionFinding puts one derived finding on /findings whose measured
// sentence ends in an unbroken 96-character session id — the shape reported
// in R2C-196, at the length the issue asks the page to survive.
func seedSessionFinding(f *fakeStore) {
	f.derived = []DerivedFinding{{
		Ecosystem:   "pypi",
		Subject:     "httpx@0.28.1",
		Believed:    "a client session is identified by a short opaque handle",
		Measured:    "the transport echoes the whole session id back, unshortened: " + longSessionToken,
		SampleID:    "sha256:aaaa000000000000000000000000000000000000000000000000000000000196",
		Environment: "python · linux/x64",
	}}
}

// seedSessionRecord gives /records a package whose latest release is a Go
// pseudo-version — 36 characters that hold no break opportunity — plus the
// long token, so the same rule is measured at the length the issue names.
func seedSessionRecord(f *fakeStore) {
	f.packages = []PackageHit{{
		Ecosystem:     "golang",
		Name:          "github.com/r2cuerdame/" + longSessionToken,
		LatestVersion: "v0.0.0-20240606120523-5a60cdf6a761",
		Symbols:       4,
		EvidenceCount: 12,
		UpdatedAt:     "2026-08-24",
	}}
}

const cardMeasurePath = "/__card-overflow-measure"

// cardHarness serves the site under test plus one page that loads `target`
// in a fixed-width iframe per viewport and reports the geometry. The iframe
// is what makes the narrow widths measurable at all: a headless Chrome
// window will not size itself below ~500 CSS px, and an iframe has no floor.
func cardHarness(mux *http.ServeMux, target, cardSel string) http.Handler {
	widths := make([]string, 0, len(cardViewports))
	frames := make([]string, 0, len(cardViewports))
	for _, w := range cardViewports {
		widths = append(widths, fmt.Sprint(w))
		frames = append(frames, fmt.Sprintf(
			`<iframe id="c%d" width="%d" height="1400" src="%s"></iframe>`, w, w, html.EscapeString(target)))
	}
	page := strings.NewReplacer(
		"__WIDTHS__", strings.Join(widths, ","),
		"__FRAMES__", strings.Join(frames, "\n"),
		"__CARDSEL__", cardSel,
		"__TOKEN__", longSessionToken,
	).Replace(cardHarnessHTML)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != cardMeasurePath {
			mux.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
}

// cardHarnessHTML reports, per viewport: the document width, every unclipped
// box that reaches past it, every card, and every element holding the token.
//
// An element only counts as an offender if nothing between it and the
// document clips overflow — a table meant to scroll inside .tablewrap is not
// a page overflow. body is skipped as a clipper on purpose: its
// overflow-x:hidden propagates to the viewport, so it removes the scrollbar
// without removing the overflow, which is how this class of bug reaches
// production looking fine.
const cardHarnessHTML = `<!doctype html>
<meta charset="utf-8"><title>card overflow measure</title>
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
  return {sel: label(el), left: Math.round(r.left + win.scrollX), right: Math.round(r.right + win.scrollX),
          width: Math.round(r.width), inner: el.clientWidth, content: el.scrollWidth, host: host,
          fullToken: text.indexOf(TOKEN) !== -1, text: text.slice(0, 60)};
}
function clipped(el, doc, win){
  for (var p = el.parentElement; p && p !== doc.documentElement && p !== doc.body; p = p.parentElement) {
    if (win.getComputedStyle(p).overflowX !== 'visible') return true;
  }
  return false;
}
/* The token's element is the DEEPEST one that still holds all of it: an
   ancestor holds it too, and asking whether <body> wraps its text answers
   nothing. */
function tokenBoxes(doc, win){
  var out = [], all = doc.querySelectorAll('body *');
  for (var i = 0; i < all.length; i++) {
    var el = all[i];
    if ((el.textContent || '').indexOf(TOKEN) === -1) continue;
    var deeper = false;
    for (var j = 0; j < el.children.length; j++) {
      if ((el.children[j].textContent || '').indexOf(TOKEN) !== -1) { deeper = true; break; }
    }
    if (!deeper) out.push(box(el, win));
  }
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
  var cards = [], found = doc.querySelectorAll('__CARDSEL__');
  for (var k = 0; k < found.length; k++) cards.push(box(found[k], win));
  return {scrollWidth: de.scrollWidth, clientWidth: cw, offenders: offenders.slice(0, 12),
          cards: cards.slice(0, 12), tokens: tokenBoxes(doc, win)};
}
function run(){
  var out = [], widths = [__WIDTHS__];
  for (var i = 0; i < widths.length; i++) {
    var w = widths[i], fr = document.getElementById('c' + w);
    out.push({width: w, state: state(fr.contentDocument, fr.contentWindow)});
  }
  document.getElementById('measure').textContent = JSON.stringify(out);
}
window.addEventListener('load', function(){ setTimeout(run, 400); });
</script>
`

func measureCards(t *testing.T, chrome, url string) []cardReport {
	t.Helper()
	payload := renderMeasurement(t, chrome, url)
	var reports []cardReport
	if err := json.Unmarshal([]byte(payload), &reports); err != nil {
		t.Fatalf("measurement %q: %v", payload, err)
	}
	if len(reports) != len(cardViewports) {
		t.Fatalf("measured %d viewports, want %d", len(reports), len(cardViewports))
	}
	return reports
}

// TestNarrowCardRulesSurviveWithoutABrowser keeps the four fixes legible to
// someone reading the stylesheet, and keeps them asserted on a machine with
// no Chrome. It is strictly weaker than the render above — it says the rules
// are present, never that the cards fit — and exists because a skipped
// geometry check must not read as a passing one.
func TestNarrowCardRulesSurviveWithoutABrowser(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	css := get(t, mux, "/static/site.css")
	if css.Code != http.StatusOK {
		t.Fatalf("css status %d", css.Code)
	}
	sheet := css.Body.String()

	for _, want := range []string{
		// A card track floor may not exceed the width the viewport offers:
		// a flat 17rem minimum is wider than a 320px phone's content column.
		"repeat(auto-fit, minmax(min(17rem, 100%), 1fr))",
		// Believed/measured prose quotes identifiers, and an identifier has
		// no break opportunity in it.
		".finding-card p { margin: 0.7rem 0 0; font-size: 0.86rem; overflow-wrap: anywhere; }",
		// A panel heading on a phone may shrink and wrap; on a desktop it
		// still may not, which is what keeps that layout as it was.
		".home-detail .eyebrow { flex: 0 1 auto; min-width: 0; overflow-wrap: anywhere; }",
		// The landing coverage table needs the scroll container its class
		// name promised — .tablewrap was the styled one, .table-wrap was not.
		".table-wrap { overflow-x: auto; }",
		// A release pill holding a Go pseudo-version has to be allowed to
		// wrap once the row is narrower than the version is long.
		".record-version { white-space: normal; overflow-wrap: anywhere; }",
	} {
		mustContain(t, sheet, want)
	}

	// The /findings page keeps its rules inline, next to the markup they
	// style, so they are asserted from the page rather than the stylesheet.
	body := get(t, mux, "/findings").Body.String()
	for _, want := range []string{
		// One column that may be narrower than its content, not one sized
		// by the longest unbroken token in the list.
		"grid-template-columns: minmax(0, 1fr);",
		".finding { display: flex; flex-direction: column; gap: 0.35rem; min-width: 0; }",
		// ...and the token wraps rather than being cut, because in a
		// measurement the identifier is the finding.
		".finding p.claim { margin: 0; max-width: 74ch; overflow-wrap: anywhere; }",
	} {
		mustContain(t, body, want)
	}
}
