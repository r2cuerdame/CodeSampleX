package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// The seven network counters (§14.5).
	for _, label := range []string{"Peers", "Packages", "Symbols", "Evidence",
		"Verified Samples", "Post-hit success rate", "Estimated reasoning avoided"} {
		mustContain(t, body, label)
	}
	// The reasoning-avoided number is ALWAYS labeled estimated.
	mustContain(t, body, "estimated")
	// Counter values from the fake stats.
	mustContain(t, body, "45,213")
	mustContain(t, body, "87%")
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
	rec := get(t, mux, "/stats?lang=ko")
	body := rec.Body.String()
	mustContain(t, body, "네트워크 통계")
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "csx_lang=ko") {
		t.Errorf("missing lang cookie, got %q", rec.Header().Get("Set-Cookie"))
	}
	// Cookie alone selects the language on later requests.
	rec2 := get(t, mux, "/stats", "Cookie", "csx_lang=ko")
	mustContain(t, rec2.Body.String(), "네트워크 통계")
	// Accept-Language fallback.
	rec3 := get(t, mux, "/stats", "Accept-Language", "ja,en;q=0.5")
	mustContain(t, rec3.Body.String(), "ネットワーク統計")
}

func TestStatsPageEstimatedLabel(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/stats").Body.String()
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
