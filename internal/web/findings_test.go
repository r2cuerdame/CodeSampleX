package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

func TestFindingsPageRenders(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/findings")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "What the network found")
	mustContain(t, body, "Contradicts an official source")
	mustContain(t, body, "Widely believed, measured otherwise")
	// The page is only worth anything if every claim carries its sample
	// link, so spot-check one from each group.
	mustContain(t, body,
		"/samples/sha256:0c0a0329b6b2d901cce677b9664dd22b748c2a5249af00e202c0d2e12eab06f0")
	mustContain(t, body,
		"/samples/sha256:e0a5abccbe96dde0a46b9b65aae94c3d5ffaa94f5eac58c72b82bec2141a0bfa")
	mustContain(t, body, "https://codesamplex.dev/findings") // canonical
	mustContain(t, body, ".finding.panel { padding: 0.9rem 1rem; border-radius: 0.85rem; }")
}

// A finding without a live sample is an opinion, so the invariant the page
// depends on is structural: every entry carries a well-formed sample id.
// (Liveness itself is checked out of band against GET /v1/samples/<id> —
// a unit test may not reach the network.)
func TestEveryFindingCarriesASampleID(t *testing.T) {
	id := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	for _, group := range []struct {
		name string
		list []finding
	}{
		{"documented", documentedFindings},
		{"believed", believedFindings},
	} {
		for i, f := range group.list {
			if !id.MatchString(f.SampleID) {
				t.Errorf("%s[%d] (%s): bad sample id %q", group.name, i, f.Subject, f.SampleID)
			}
			if strings.TrimSpace(f.Believed) == "" || strings.TrimSpace(f.Measured) == "" {
				t.Errorf("%s[%d] (%s): needs both halves", group.name, i, f.Subject)
			}
			if f.Ecosystem == "" || f.Subject == "" {
				t.Errorf("%s[%d]: missing ecosystem or subject", group.name, i)
			}
		}
	}
	// The leading group earns its place by quoting a checkable document.
	for i, f := range documentedFindings {
		if f.SourceURL == "" || f.SourceLabel == "" {
			t.Errorf("documented[%d] (%s): needs a source the reader can open", i, f.Subject)
		}
	}
}

// A finding can cite an official document from either group: leading the
// page takes a measurement that contradicts the document, but an entry
// whose belief merely comes from one still carries the link so the reader
// can check that half too. Both groups therefore have to render it.
func TestEverySourceLinkIsRendered(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/findings").Body.String()
	sourced := 0
	for _, list := range [][]finding{documentedFindings, believedFindings} {
		for _, f := range list {
			if f.SourceURL == "" {
				continue
			}
			sourced++
			if !strings.Contains(body, f.SourceURL) {
				t.Errorf("%s: source %s is not on the page", f.Subject, f.SourceURL)
			}
		}
	}
	// Guards against the citations quietly vanishing from the folklore group,
	// which is where two of them now live.
	if sourced < 4 {
		t.Errorf("only %d findings cite a source, want at least 4", sourced)
	}
}

// The findings stay in English on purpose; the chrome does not. Every key
// the template asks for has to exist in every locale, or the page falls
// back to printing the key itself.
func TestFindingsChromeIsTranslated(t *testing.T) {
	keys := []string{
		"findings.title", "findings.intro", "findings.rerun",
		"findings.untranslated", "findings.count", "findings.group_docs",
		"findings.group_docs_note", "findings.group_belief",
		"findings.group_belief_note", "findings.believed", "findings.measured",
		"findings.contract", "findings.source", "findings.method_heading",
		"findings.method_body", "findings.method_missing", "meta.findings",
		"nav.findings",
	}
	for _, lang := range i18n.Supported {
		for _, k := range keys {
			if got := i18n.T(lang, k); got == k {
				t.Errorf("%s: missing translation for %q", lang, k)
			}
		}
	}
}

func TestFindingsFollowsTheLanguageSwitch(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/findings?lang=ko")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, i18n.T("ko", "findings.title"))
	// The findings themselves are not translated, and the page says so.
	mustContain(t, body, "deny_unknown_fields")
	// Sample links carry the chosen language onward.
	mustContain(t, body, "lang=ko")
}
