package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/mcp"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// The count used to be the literal 8, and the name said "Eight". The server
// grew to ten tools and neither the page nor this test noticed, because both
// were hand-kept lists that agreed with each other. It asks the server now.
func TestFeaturesPageDocumentsThePublicMCPTools(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/features")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()

	mustContain(t, body, `<link rel="canonical" href="https://codesamplex.dev/features">`)
	mustContain(t, body, `href="/features">Features</a>`)
	mustContain(t, body, `id="feature-summary-heading"`)
	if want := len(mcp.ToolNames()); strings.Count(body, `<details class="feature-page__tool"`) != want {
		t.Fatalf("public MCP tool details = %d, want %d — the page claims to be complete",
			strings.Count(body, `<details class="feature-page__tool"`), want)
	}
	if strings.Contains(body, `<details class="feature-page__tool" open`) {
		t.Error("tool details must be collapsed initially")
	}

	for _, name := range []string{
		"search_known_solution",
		"get_sample",
		"explain_compatibility",
		"run_observed_command",
		"report_sample_adoption",
		"propose_public_sample",
		"list_local_hits",
		"get_local_stats",
	} {
		if got := strings.Count(body, `id="tool-`+name+`"`); got != 1 {
			t.Errorf("tool %s detail count = %d, want 1", name, got)
		}
	}

	for _, exactSchemaFact := range []string{
		`<code>query</code> <span>string</span>`,
		`<code>errorText</code> <span>string</span>`,
		`<code>sampleId</code> <span>string</span>`,
		`<code>command</code> <span>string[]</span>`,
		`<code>cwd</code> <span>string</span>`,
		`<code>offerId</code> <span>string</span>`,
		`<code>applied</code> <span>boolean</span>`,
		`<code>buildPass</code> <span>boolean</span>`,
		`<code>goal</code> <span>string</span>`,
		`<code>packages</code> <span>string[]</span>`,
		`&#34;evidenceClass&#34;: &#34;USAGE_OBSERVATION&#34;`,
		`&#34;publishRequiresUserApproval&#34;: true`,
	} {
		mustContain(t, body, exactSchemaFact)
	}

	for _, internalOnly := range []string{"sample_worker", "seeder token", "admin API"} {
		if strings.Contains(strings.ToLower(body), internalOnly) {
			t.Errorf("public features page exposes internal-only term %q", internalOnly)
		}
	}
}

func TestFeaturesPageChromeIsLocalizedForEveryLocale(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, lang := range i18n.Supported {
		rec := get(t, mux, "/features?lang="+lang)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status %d", lang, rec.Code)
		}
		body := rec.Body.String()
		mustContain(t, body, `<html lang="`+lang+`">`)
		mustContain(t, body, `>`+i18n.T(lang, "features.title")+`</h1>`)
		mustContain(t, body, `href="/features`)
		mustContain(t, body, i18n.T(lang, "features.privacy"))
		if strings.Contains(body, ">features.") || strings.Contains(body, `"features.`) {
			t.Errorf("%s rendered a translation key", lang)
		}
		canonical := canonicalOf(body)
		if lang == i18n.Default {
			if canonical != "https://codesamplex.dev/features" {
				t.Errorf("%s canonical = %q", lang, canonical)
			}
		} else if canonical != "https://codesamplex.dev/features?lang="+lang {
			t.Errorf("%s canonical = %q", lang, canonical)
		}
	}
}

func TestFeaturesPageKoreanLocalizesToolDocumentation(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/features?lang=ko").Body.String()
	for _, want := range []string{
		"증거 찾기와 확인",
		"실행하며 관측하기",
		"증거 순환 완성하기",
		"이 설치 상태 확인하기",
		"설명한 환경을 기준으로 등급이 매겨진 검증 해법을 찾습니다.",
		"3 도구",
	} {
		mustContain(t, body, want)
	}
	for _, englishLeak := range []string{
		"Find and inspect evidence",
		"Run with observation",
		"Close the evidence loop",
		"Inspect this installation",
		"What you are trying to do or fix, in plain words.",
		"8 tools",
	} {
		if strings.Contains(body, englishLeak) {
			t.Errorf("Korean feature page leaked English copy %q", englishLeak)
		}
	}
}

func TestFeaturesRouteSEOAndResponsiveLayout(t *testing.T) {
	mux, _ := newTestMux(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/features/?lang=ko", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/features?lang=ko" {
		t.Fatalf("trailing slash = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	sitemap := sitemapBody(t, mux)
	mustContain(t, sitemap, `<loc>https://codesamplex.dev/features</loc>`)

	css := get(t, mux, "/static/site.css").Body.String()
	for _, want := range []string{
		`.feature-page {`,
		`.feature-page__json-grid pre {`,
		`white-space: pre-wrap;`,
		`overflow-wrap: anywhere;`,
		`.feature-page__json-grid { grid-template-columns: 1fr;`,
	} {
		mustContain(t, css, want)
	}
}

func TestLandingOnlyAddsTheFeaturesNavigationLink(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()
	mustContain(t, body, `href="/features">Features</a>`)
	if strings.Contains(body, `id="feature-summary-heading"`) || strings.Contains(body, `id="tool-search_known_solution"`) {
		t.Error("feature page content leaked into the homepage")
	}
}
