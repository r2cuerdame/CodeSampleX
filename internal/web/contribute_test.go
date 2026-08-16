package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// The publish endpoint refuses anonymous uploads with a message naming four
// open channels and pointing here. This is the other half of that promise:
// if the page stops naming them, the refusal sends people to a page that
// does not answer them.
func TestContributePageAnswersTheRefusal(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/contribute")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// The four channels the 403 names, in the reader's language.
	for _, key := range []string{
		"contribute.evidence_h", "contribute.verify_h",
		"contribute.report_h", "contribute.ask_h",
	} {
		mustContain(t, body, i18n.T("en", key))
	}
	// And where each one actually goes. A channel named without a way to
	// reach it is a paragraph, not a channel.
	for _, href := range []string{"/wanted", "/adapters", "github.com"} {
		mustContain(t, body, href)
	}
	// The claim the whole policy exists to make.
	mustContain(t, body, i18n.T("en", "contribute.claim"))
	mustContain(t, body, "https://codesamplex.dev/contribute") // canonical
}

// The page is translated like every other page, and the claim is the one
// sentence that must not silently fall back to English.
func TestContributePageIsTranslated(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/contribute?lang=ko").Body.String()
	if strings.Contains(body, i18n.T("en", "contribute.claim")) {
		t.Error("the Korean page is serving the English claim")
	}
	mustContain(t, body, i18n.T("ko", "contribute.claim"))
}
