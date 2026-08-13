package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func newTestMux(t *testing.T, mutate func(*Deps)) (*http.ServeMux, *fakeStore) {
	t.Helper()
	f := newFakeStore()
	d := Deps{
		Store:     f,
		PublicURL: "https://codesamplex.dev",
		Version:   "1.0.0-test",
	}
	if mutate != nil {
		mutate(&d)
	}
	mux := http.NewServeMux()
	Register(mux, d)
	return mux, f
}

func get(t *testing.T, mux *http.ServeMux, target string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func mustContain(t *testing.T, body, sub string) {
	t.Helper()
	if !strings.Contains(body, sub) {
		t.Errorf("body missing %q\n----\n%s", sub, truncate(body))
	}
}

func truncate(s string) string {
	if len(s) > 4000 {
		return s[:4000] + "…"
	}
	return s
}

func TestLandingEnglish(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "Stop solving the same code twice.")
	mustContain(t, body, "irm https://codesamplex.dev/install.ps1 | iex")
	mustContain(t, body, "curl -fsSL https://codesamplex.dev/install.sh | sh")
	// One page carries the whole story: the counters, the way in by name,
	// and what the network can observe in each ecosystem.
	for _, s := range []string{"Peers", "Verified Samples", "Estimated reasoning avoided", "45,213",
		"What it can observe today", "npm", "packages &amp; versions"} {
		mustContain(t, body, s)
	}
	// Project links belong on every page.
	mustContain(t, body, "https://github.com/r2cuerdame/CodeSampleX")
	mustContain(t, body, "https://github.com/sponsors/r2cuerdame")
	// Flywheel copy.
	mustContain(t, body, "More users")
	mustContain(t, body, "Better answers")
	// §5.4 contract table verbatim.
	for _, s := range []string{"You get", "You contribute", "Never shared automatically",
		"Public compatibility knowledge", "Sanitized failure fingerprints",
		"Secrets or environment variables", "Raw compiler/runtime logs"} {
		mustContain(t, body, s)
	}
	// No external assets: no http(s) stylesheet/script/font references.
	if strings.Contains(body, "cdn.") || strings.Contains(body, "googleapis") {
		t.Error("landing references external assets")
	}
}

// TestEnglishReachableFromAnotherLanguage pins the fix for a switcher that
// could not switch: /en/ used to redirect to "/", which re-negotiates
// Accept-Language and sent a Korean browser straight back to Korean.
func TestEnglishReachableFromAnotherLanguage(t *testing.T) {
	mux, _ := newTestMux(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Language", "ko-KR,ko;q=0.9,en;q=0.8")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `lang="ko"`) {
		t.Fatal("Korean browser should get Korean at /")
	}
	// The switcher must offer an explicit English URL, never bare "/".
	mustContain(t, rec.Body.String(), `href="/en/"`)

	req = httptest.NewRequest(http.MethodGet, "/en/", nil)
	req.Header.Set("Accept-Language", "ko-KR,ko;q=0.9,en;q=0.8")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/en/ status = %d, want 200 (a redirect to / re-negotiates)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `lang="en"`) || !strings.Contains(body, "Stop solving the same code twice.") {
		t.Errorf("/en/ did not render English:\n%s", truncate(body))
	}
	// The choice sticks, so the next page is not re-negotiated back.
	var stuck bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == langCookie && c.Value == "en" {
			stuck = true
		}
	}
	if !stuck {
		t.Error("visiting /en/ must remember the choice in the language cookie")
	}
}

// TestStatsPageRendersProducerJSON feeds the page the exact document the
// aggregator writes. A field-shape mismatch (estimatedReasoningAvoided is
// an object, not a number) silently failed the whole decode and blanked
// every counter on the live site while the API returned real values.
func TestStatsPageRendersProducerJSON(t *testing.T) {
	produced, err := compatibility.StatsJSON(serverstore.NetworkCounts{
		Peers: 2, Packages: 76, Symbols: 5, Observations: 312, VerifiedSamples: 4,
	}, 7, time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	mux, store := newTestMux(t, nil)
	store.statsJSON, store.statsOK = string(produced), true

	body := get(t, mux, "/explore").Body.String()
	for _, want := range []string{"76", "312", "5", "4", "21"} { // 21 = 7 hits × 3
		if !strings.Contains(body, want) {
			t.Errorf("counter %q missing from /explore — page shows placeholders instead of the aggregator's numbers:\n%s",
				want, truncate(body))
		}
	}
	if strings.Contains(body, `<span class="num mono">—</span>`) {
		t.Error("a counter rendered as — while the aggregator supplied a value")
	}
}

// TestStatsPathRedirectsToExplore keeps old links and indexed URLs alive
// after the counters moved onto the explorer.
func TestStatsPathRedirectsToExplore(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/stats")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("/stats status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/explore" {
		t.Errorf("Location = %q, want /explore", loc)
	}
	// And the nav no longer carries a Stats entry.
	body := get(t, mux, "/").Body.String()
	if strings.Contains(body, `href="/stats"`) {
		t.Error("nav still links to the removed stats page")
	}
}

// The pipeline explanation lives on the landing page, where a first-time
// visitor meets it before deciding to install anything.
func TestLandingExplainsHowItWorks(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()
	mustContain(t, body, "How it works")
	mustContain(t, body, "never leave your machine")
	// Plain-language answer to "what is this".
	mustContain(t, body, "What is CodeSampleX?")
}

func TestAdaptersPageIsReadable(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/adapters").Body.String()
	// No separate legend: the cards carry the meanings, and a key that
	// repeats them is one more thing to read.
	if strings.Contains(body, "How to read this table") {
		t.Error("legend table is redundant with the per-card level text")
	}
	// Each level is spelled out next to its code on the adapter card, so a
	// reader never has to decode a row of bare ✓/— glyphs.
	for _, s := range []string{"A0", "A4", "detected from the lockfile", "verified against a contract"} {
		mustContain(t, body, s)
	}
	// A missing capability is stated in words too, not only as a glyph.
	mustContain(t, body, "not supported")
	// Symbol confidence explains itself in place and on hover.
	mustContain(t, body, "PROBABLE — Resolved from imports and call sites")
}

func TestLandingKorean(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/ko/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "같은 코드를 두 번 풀지 마세요.")
	mustContain(t, body, `lang="ko"`)
	// Localized contract heading.
	mustContain(t, body, "자동으로 공유되지 않는 것")
	// Data values stay as-is (install line unchanged).
	mustContain(t, body, "irm https://codesamplex.dev/install.ps1 | iex")
}

func TestLandingHreflangAlternates(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()
	if n := strings.Count(body, `hreflang="`); n != 10 {
		t.Errorf("want 10 hreflang links (9 locales + x-default), got %d", n)
	}
	mustContain(t, body, `hreflang="ko" href="https://codesamplex.dev/ko/"`)
	mustContain(t, body, `hreflang="x-default" href="https://codesamplex.dev/"`)
	mustContain(t, body, `rel="canonical" href="https://codesamplex.dev/"`)
	// JSON-LD present.
	mustContain(t, body, `"WebSite"`)
	mustContain(t, body, `"SoftwareApplication"`)
}

func TestLandingRedirectBareLang(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/ko")
	if rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("want redirect for /ko, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ko/" {
		t.Errorf("Location = %q", loc)
	}
}

func TestLangQueryAndCookie(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/explore?lang=ko")
	body := rec.Body.String()
	mustContain(t, body, "패키지 탐색")
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "csx_lang=ko") {
		t.Errorf("missing lang cookie, got %q", rec.Header().Get("Set-Cookie"))
	}
	// Cookie alone selects the language on later requests.
	rec2 := get(t, mux, "/explore", "Cookie", "csx_lang=ko")
	mustContain(t, rec2.Body.String(), "패키지 탐색")
	// Accept-Language fallback.
	rec3 := get(t, mux, "/explore", "Accept-Language", "ja,en;q=0.5")
	mustContain(t, rec3.Body.String(), "パッケージ探索")
}

func TestNetworkCountersEstimatedLabel(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/explore").Body.String()
	mustContain(t, body, "Estimated reasoning avoided")
	mustContain(t, body, "estimated")
	mustContain(t, body, "1,204")
}

func TestRobotsTxt(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/robots.txt")
	body := rec.Body.String()
	mustContain(t, body, "User-agent: *")
	mustContain(t, body, "Allow: /")
	mustContain(t, body, "Sitemap: https://codesamplex.dev/sitemap.xml")
}

func TestSitemap(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/sitemap.xml")
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Errorf("content type %q", ct)
	}
	body := rec.Body.String()
	// Per-locale landing entries with alternates.
	mustContain(t, body, "<loc>https://codesamplex.dev/ko/</loc>")
	mustContain(t, body, `hreflang="ja"`)
	// Hot packages and adapters page.
	mustContain(t, body, "<loc>https://codesamplex.dev/npm/axios</loc>")
	mustContain(t, body, "<loc>https://codesamplex.dev/adapters</loc>")
}

func TestInstallScripts(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	ps := get(t, mux, "/install.ps1").Body.String()
	mustContain(t, ps, "$base = 'https://codesamplex.dev'")
	mustContain(t, ps, "$base/dl/csx-windows-$arch.exe")
	mustContain(t, ps, "csx.exe")
	mustContain(t, ps, "init")
	if strings.Contains(ps, "__CSX_BASE_URL__") {
		t.Error("install.ps1 placeholder not substituted")
	}
	sh := get(t, mux, "/install.sh").Body.String()
	mustContain(t, sh, `base="https://codesamplex.dev"`)
	mustContain(t, sh, "/dl/csx-$os-$arch")
	mustContain(t, sh, "init")
	mustContain(t, sh, ".local/bin")
}

func TestDownloadAndTraversal(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "csx-windows-amd64.exe"), []byte("MZbinary"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dist, "..", "secret.txt")
	if err := os.WriteFile(secret, []byte("topsecret"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux, _ := newTestMux(t, func(d *Deps) { d.DistDir = dist })

	if rec := get(t, mux, "/dl/csx-windows-amd64.exe"); rec.Code != http.StatusOK {
		t.Errorf("valid file status %d", rec.Code)
	} else if rec.Body.String() != "MZbinary" {
		t.Errorf("body %q", rec.Body.String())
	}
	// Encoded traversal must 404 (the mux decodes %2F into the segment).
	if rec := get(t, mux, "/dl/..%2Fsecret.txt"); rec.Code != http.StatusNotFound {
		t.Errorf("traversal status %d body %q", rec.Code, rec.Body.String())
	}
	if rec := get(t, mux, "/dl/%2e%2e%5csecret.txt"); rec.Code != http.StatusNotFound {
		t.Errorf("backslash traversal status %d", rec.Code)
	}
	if rec := get(t, mux, "/dl/missing-file"); rec.Code != http.StatusNotFound {
		t.Errorf("missing file status %d", rec.Code)
	}
	// DistDir unset ⇒ always 404.
	mux2, _ := newTestMux(t, nil)
	if rec := get(t, mux2, "/dl/csx-windows-amd64.exe"); rec.Code != http.StatusNotFound {
		t.Errorf("unset DistDir status %d", rec.Code)
	}
}

func TestStaticCSSServed(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/static/site.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/css") {
		t.Errorf("content type %q", rec.Header().Get("Content-Type"))
	}
	mustContain(t, rec.Body.String(), "prefers-color-scheme")
}

func TestStatsUnavailableStillRenders(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) {
		f := newFakeStore()
		f.statsOK = false
		d.Store = f
	})
	rec := get(t, mux, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("landing must render without stats, got %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Stop solving the same code twice.")
}
