package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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

func mustNotContain(t *testing.T, body, sub string) {
	t.Helper()
	if strings.Contains(body, sub) {
		t.Errorf("body unexpectedly contains %q", sub)
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
	mustContain(t, body, "Does it run there?")
	mustContain(t, body, `class="landing-page"`)
	if strings.Contains(body, `class="hero-art"`) || strings.Contains(body, `<img src="/static/inspector-hero-v1.webp"`) {
		t.Error("landing still renders the old hero illustration")
	}
	// The existing social card remains stable even though the illustration no
	// longer competes with search and measured evidence in the visible page.
	mustContain(t, body, `content="https://codesamplex.dev/static/inspector-hero-v1.webp"`)
	mustContain(t, body, `content="summary_large_image"`)
	mustContain(t, body, `class="home-search" action="/records"`)
	// The matrix IS the example now; the old hero example card is gone.
	if strings.Contains(body, `class="evidence-question"`) {
		t.Error("landing still renders the decorative hero example card")
	}
	if strings.Contains(body, "pnpm + Windows 11") {
		t.Error("landing still advertises the old environment example that has no matching evidence page")
	}
	mustContain(t, body, "irm https://codesamplex.dev/install.ps1 | iex")
	mustContain(t, body, "curl -fsSL https://codesamplex.dev/install.sh | sh")
	// One page carries the whole story: the focused counters and the
	// measured ecosystems linked straight into the records inventory.
	for _, s := range []string{"Packages", "APIs covered", "1.2K",
		`title="1,200" aria-label="APIs covered: 1,200"`,
		`class="ecorow`, `href="/records?eco=npm"`, `href="/records?eco=maven"`} {
		mustContain(t, body, s)
	}
	if got := strings.Count(body, `<div class="stat">`); got != 2 {
		t.Errorf("homepage stat cards = %d, want exactly 2", got)
	}
	for _, omitted := range []string{"Symbols", "Projects this month", "Peers today", "Post-hit success rate"} {
		if strings.Contains(body, `<span class="lbl">`+omitted+`</span>`) {
			t.Errorf("homepage still renders %q as a stat card", omitted)
		}
	}
	// Project links belong on every page.
	mustContain(t, body, "https://github.com/r2cuerdame/CodeSampleX")
	mustContain(t, body, "https://github.com/sponsors/r2cuerdame")
	// The grid legend explains cells, and only cells: the sample-status
	// ladder belongs where samples are, not under a compatibility grid.
	mustContain(t, body, "How to read the grid")
	for _, s := range []string{"USAGE_OBSERVATION", "SAMPLE_VERIFICATION", "CROSS_PASS", "MATRIX_PASS"} {
		if strings.Contains(body, s) {
			t.Errorf("sample-status vocabulary %q leaked into the grid legend", s)
		}
	}
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

func TestLandingPutsSearchAndEvidenceBeforeInstallationAndSupport(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()
	ordered := []string{
		`class="hero home-hero"`,
		`class="home-search"`,
		`id="measured"`,
		`id="install"`,
		`id="agents"`,
	}
	last := -1
	for _, marker := range ordered {
		at := strings.Index(body, marker)
		if at < 0 {
			t.Fatalf("landing hierarchy is missing %q", marker)
		}
		if at <= last {
			t.Fatalf("landing hierarchy out of order at %q", marker)
		}
		last = at
	}
	for _, workerOnly := range []string{`id="install-worker"`, `csx worker start --mode verify`} {
		if strings.Contains(body, workerOnly) {
			t.Errorf("worker-only contribution content leaked onto the homepage: %q", workerOnly)
		}
	}
	mustContain(t, body, `<details id="agents" class="home-detail agent-detail support-agents">`)
	if strings.Contains(body, `id="agents" class="home-detail agent-detail support-agents" open`) {
		t.Error("agent integration details must be collapsed by default")
	}
	if strings.Contains(body, "eyebrow-mark") {
		t.Error("hero repeats the CodeSampleX brand above the tagline")
	}
}

func TestLandingWithoutFeaturedFindingsHasNoBrokenMeasuredAnchor(t *testing.T) {
	var out bytes.Buffer
	page := landingPage{
		basePage:  basePage{Lang: "en", Title: "CodeSampleX", IsLanding: true},
		InstallPS: "install-windows",
		InstallSH: "install-unix",
		LLMPrompt: "install with an agent",
		Findings:  nil,
	}
	if err := parseTemplates()["landing"].ExecuteTemplate(&out, "base.html", page); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if strings.Contains(body, `href="#measured"`) {
		t.Error("empty findings render links to an absent #measured section")
	}
	mustContain(t, body, `class="action primary" href="#matrix"`)
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
	if !strings.Contains(body, `lang="en"`) || !strings.Contains(body, "Does it run there?") {
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
// aggregator writes. The homepage displays only the three decision-useful
// counters in compact form, while preserving each exact count for assistive
// technology and hover text.
func TestStatsPageRendersProducerJSON(t *testing.T) {
	produced, err := compatibility.StatsJSON(serverstore.NetworkCounts{
		Peers: 22, Packages: 17_500, Symbols: 9_876, Observations: 45_213, VerifiedSamples: 1_234,
	}, serverstore.AdoptionCounts{Reports: 7, Applied: 7}, time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	mux, store := newTestMux(t, nil)
	store.statsJSON, store.statsOK = string(produced), true

	body := get(t, mux, "/").Body.String()
	for _, want := range []string{
		`title="17,500" aria-label="Packages: 17,500">17.5K</span>`,
		`title="9,876" aria-label="APIs covered: 9,876">9.9K</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("counter %q missing from the front page:\n%s",
				want, truncate(body))
		}
	}
	if got := strings.Count(body, `<div class="stat">`); got != 2 {
		t.Errorf("homepage stat cards = %d, want exactly 2", got)
	}
	// The guard is "exactly three, and they are coverage". It used to forbid
	// the symbol count as a fourth card; the API count IS one of the three
	// now, and what must not come back is the raw volume figure — a build-run
	// total means whatever its population means, and reporters are anonymous
	// by design so the page can never say which.
	if strings.Contains(body, `<span class="lbl">Evidence</span>`) || strings.Contains(body, `>45.2K</span>`) {
		t.Errorf("a raw volume counter returned to the homepage:\n%s", truncate(body))
	}
	if n := strings.Count(body, `<span class="num mono">—</span>`); n != 0 {
		t.Errorf("%d counters rendered as a placeholder, want none:\n%s",
			n, truncate(body))
	}
}

// TestStatsPathRedirects keeps old links and indexed URLs alive after
// the counters moved onto the front page.
func TestStatsPathRedirects(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/stats")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("/stats status = %d, want 301", rec.Code)
	}
	// Straight to the destination: /explore is itself a 301 to /records,
	// so pointing here made /stats a two-hop redirect chain.
	if loc := rec.Header().Get("Location"); loc != "/records" {
		t.Errorf("Location = %q, want /records", loc)
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

// The hero's ecosystem row is an inventory, not a capability table: every
// routed ecosystem is named and linked, and no A-level codes leak in.
func TestLandingEcosystemRowIsAPlainInventory(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()
	for _, ecosystem := range landingEcosystems {
		mustContain(t, body, `href="/records?eco=`+ecosystem+`"`)
	}
	if strings.Contains(body, ">A0<") || strings.Contains(body, ">A4<") {
		t.Error("landing shows raw capability codes")
	}
}

// TestLandingNamesSupportedAgents: "does this work with what I already
// use" is answered by naming the agents, in every locale — these are the
// words people search for, so they must survive template edits and must
// not get translated away into a generic phrase.
func TestLandingNamesSupportedAgents(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, path := range []string{"/", "/ko/", "/ja/", "/zh-CN/", "/de/", "/es/", "/fr/", "/pt-BR/", "/ru/"} {
		body := get(t, mux, path).Body.String()
		for _, agent := range []string{
			"Claude Code", "Codex", "Gemini CLI", "OpenCode", // auto-registered
			"Cursor", "Windsurf", "Zed", "VS Code", // any MCP client
			"Claude", "GPT", "Gemini", "Llama", // model-agnostic
		} {
			if !strings.Contains(body, agent) {
				t.Errorf("%s does not name %q", path, agent)
			}
		}
		// The command that PRINTS the MCP config, for clients csx init
		// cannot configure. Not the config itself: the page used to hand
		// out {"command":"csx"}, and a client started by an editor does not
		// inherit the PATH that makes a bare csx resolve.
		if !strings.Contains(body, "csx mcp-config") {
			t.Errorf("%s is missing the manual MCP config command", path)
		}
		if strings.Contains(body, `"command":"csx"`) {
			t.Errorf("%s hands out the bare-name MCP config, which does not resolve", path)
		}
	}
}

func TestLandingKorean(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/ko/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "거기서도 돌아갈까?")
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
	rec := get(t, mux, "/records?lang=ko")
	body := rec.Body.String()
	mustContain(t, body, "기록")
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "csx_lang=ko") {
		t.Errorf("missing lang cookie, got %q", rec.Header().Get("Set-Cookie"))
	}
	// Cookie alone selects the language on later requests.
	rec2 := get(t, mux, "/records", "Cookie", "csx_lang=ko")
	mustContain(t, rec2.Body.String(), "기록")
	// Accept-Language fallback.
	rec3 := get(t, mux, "/records", "Accept-Language", "ja,en;q=0.5")
	mustContain(t, rec3.Body.String(), "記録")
}

func TestNetworkCountersKeepExactAccessibleValues(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()
	mustContain(t, body, `title="1,200" aria-label="APIs covered: 1,200">1.2K</span>`)
	if strings.Contains(body, `<span class="lbl">Estimated reasoning avoided</span>`) {
		t.Error("non-headline estimate still renders as a homepage card")
	}
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
	mustContain(t, body, "<loc>https://codesamplex.dev/npm/axios</loc>")
	mustContain(t, body, "<loc>https://codesamplex.dev/records</loc>")
	// Redirect-only paths stay out of the map.
	for _, gone := range []string{"/adapters", "/stats"} {
		if strings.Contains(body, "codesamplex.dev"+gone+"<") {
			t.Errorf("sitemap still advertises the redirect %q", gone)
		}
	}
}

func TestInstallScripts(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	ps := get(t, mux, "/install.ps1").Body.String()
	mustContain(t, ps, "$base = 'https://codesamplex.dev'")
	mustContain(t, ps, "$base/dl/csx-windows-$arch.exe")
	mustContain(t, ps, "csx.exe")
	mustContain(t, ps, "init")
	mustContain(t, ps, "csx-payload.new.exe")
	mustContain(t, ps, "csx-launcher.new.exe")
	mustContain(t, ps, "bootstrap-launcher")
	mustContain(t, ps, "SHA256SUMS.txt")
	mustContain(t, ps, "update adopt")
	mustContain(t, ps, "Move-Item -Path $aside -Destination $exe -Force")
	if strings.Contains(ps, "__CSX_BASE_URL__") {
		t.Error("install.ps1 placeholder not substituted")
	}
	sh := get(t, mux, "/install.sh").Body.String()
	mustContain(t, sh, `CSX_WORKER_ONLY`)
	mustContain(t, sh, `init --community --yes --no-agents --no-daemon`)
	mustContain(t, ps, `CSX_WORKER_ONLY`)
	mustContain(t, ps, `init --community --yes --no-agents --no-daemon`)
	mustContain(t, sh, `base="https://codesamplex.dev"`)
	mustContain(t, sh, "/dl/csx-$os-$arch")
	mustContain(t, sh, "init")
	mustContain(t, sh, "SHA256SUMS.txt")
	mustContain(t, sh, "update adopt")
	mustContain(t, sh, "previous install restored")
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
	mustContain(t, rec.Body.String(), ".sample-id { overflow-wrap: anywhere; word-break: break-word; }")
	mustContain(t, rec.Body.String(), ".gridpanel {")
	mustContain(t, rec.Body.String(), ".gridstats {")
	mustContain(t, rec.Body.String(), ".badge-help.open .badge-tip")
	mustContain(t, rec.Body.String(), ".samples .badge-help { position: static; }")
	mustContain(t, rec.Body.String(), ".support-shell {\n  display: grid; grid-template-columns: minmax(0, 1fr);\n  gap: 1rem; align-items: start; margin-bottom: 1rem;")
	mustContain(t, rec.Body.String(), ".how-body {\n  display: grid; grid-template-columns:")
	mustContain(t, rec.Body.String(), ".flabel {\n  display: inline-block;")
	mustContain(t, rec.Body.String(), ".record-version { padding:")
	mustContain(t, rec.Body.String(), ".record-symbols { padding:")
	mustContain(t, rec.Body.String(), "@media (hover: none), (pointer: coarse)")
	mustContain(t, rec.Body.String(), "@media (prefers-reduced-motion: reduce)")
}

func TestStaticInspectorHeroServed(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/static/inspector-hero-v1.webp")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "image/webp") {
		t.Errorf("content type %q", rec.Header().Get("Content-Type"))
	}
	if rec.Body.Len() < 1_000 {
		t.Errorf("hero asset unexpectedly small: %d bytes", rec.Body.Len())
	}
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
	body := rec.Body.String()
	mustContain(t, body, "Does it run there?")
	if got := strings.Count(body, `<div class="stat">`); got != 2 {
		t.Errorf("unavailable stats cards = %d, want 2 placeholders", got)
	}
	if got := strings.Count(body, `<span class="num mono">—</span>`); got != 2 {
		t.Errorf("unavailable stat placeholders = %d, want 2", got)
	}
}

// TopWanted lets the fake stand in for the real store on the wanted page.
func (f *fakeStore) TopWanted(_ context.Context, limit int) ([]WantedRow, error) {
	if limit > 0 && len(f.wanted) > limit {
		return f.wanted[:limit], nil
	}
	return f.wanted, nil
}

func (f *fakeStore) WantedRows(_ context.Context, query string, offset, limit int) ([]WantedRow, int, error) {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	all := append([]WantedRow(nil), f.wanted...)
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Asks != all[j].Asks {
			return all[i].Asks > all[j].Asks
		}
		if all[i].Ecosystem != all[j].Ecosystem {
			return all[i].Ecosystem < all[j].Ecosystem
		}
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		if all[i].Version != all[j].Version {
			return all[i].Version < all[j].Version
		}
		return all[i].Symbol < all[j].Symbol
	})
	matched := all[:0]
	for _, row := range all {
		haystack := strings.ToLower(strings.Join([]string{row.Ecosystem, row.Name, row.Version, row.Symbol}, " "))
		ok := true
		for _, word := range words {
			if !strings.Contains(haystack, word) {
				ok = false
				break
			}
		}
		if ok {
			matched = append(matched, row)
		}
	}
	total := len(matched)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return nil, total, nil
	}
	matched = matched[offset:]
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, total, nil
}

func (f *fakeStore) WantedForPackage(_ context.Context, ecosystem, name string) ([]WantedRow, error) {
	var out []WantedRow
	for _, row := range f.wanted {
		if row.Ecosystem == ecosystem && row.Name == name {
			out = append(out, row)
		}
	}
	return out, nil
}

// The features page documents the MCP surface, but the CLI is the product;
// it must name the CLI's own help before the tool list. And only help forms
// the CLI actually accepts may appear there.
func TestFeaturesShowsCLIHelpAboveMCP(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/features").Body.String()
	for _, s := range []string{"Start from the CLI", "csx help", "csx worker --help"} {
		mustContain(t, body, s)
	}
	cli := strings.Index(body, "Start from the CLI")
	tools := strings.Index(body, "feature-summary-heading")
	if cli < 0 || tools < 0 || cli > tools {
		t.Fatalf("CLI help must precede the MCP tool list: cli=%d tools=%d", cli, tools)
	}
}

func TestStylesheetCapsHeightsWithOverflow(t *testing.T) {
	css, err := staticFS.ReadFile("static/site.css")
	if err != nil {
		t.Fatal(err)
	}
	// Comments would otherwise ride along in the selector text and split one
	// selector into two keys, so the cascade check would miss its match.
	var stripped strings.Builder
	for rest := string(css); rest != ""; {
		i := strings.Index(rest, "/*")
		if i < 0 {
			stripped.WriteString(rest)
			break
		}
		stripped.WriteString(rest[:i])
		j := strings.Index(rest[i:], "*/")
		if j < 0 {
			break
		}
		rest = rest[i+j+2:]
	}

	type rule struct{ selector, decls string }
	var rules []rule
	scrolls := map[string]bool{}
	for _, block := range strings.Split(stripped.String(), "}") {
		open := strings.Index(block, "{")
		if open < 0 {
			continue
		}
		sel := strings.TrimSpace(block[:open])
		if i := strings.LastIndex(sel, "{"); i >= 0 {
			sel = strings.TrimSpace(sel[i+1:]) // shed an enclosing @media
		}
		decls := block[open+1:]
		if strings.Contains(decls, "overflow:") {
			scrolls[sel] = true
		}
		rules = append(rules, rule{sel, decls})
	}
	for _, r := range rules {
		if !strings.Contains(r.decls, "max-height") || scrolls[r.selector] {
			continue
		}
		// Caps on containers that only hold laid-out boxes are fine; the ones
		// that matter wrap text the reader would otherwise lose.
		if !strings.Contains(r.selector, "text") && !strings.Contains(r.selector, "pre") {
			continue
		}
		t.Errorf("%q caps max-height without declaring overflow", r.selector)
	}
}
