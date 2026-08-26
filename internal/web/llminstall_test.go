package web

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/buildinfo"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// The install box asks a visitor to pipe a stranger's script into a shell.
// The alternative — hand the same install to the agent already open in
// their editor — is only an alternative if it is reachable from the anchor
// every "Install" link on the site points at.
func TestInstallSectionOffersTheAgentDrivenPath(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()

	if got := strings.Count(body, `data-dialog-open="install-llm-dialog"`); got != 2 {
		t.Fatalf("agent install dialog triggers = %d, want hero + install section", got)
	}
	if got := strings.Count(body, `aria-controls="install-llm-dialog"`); got != 2 {
		t.Fatalf("dialog controls = %d, want 2", got)
	}
	mustContain(t, body, `aria-haspopup="dialog"`)
	mustContain(t, body, `id="install-llm-dialog"`)
	mustContain(t, body, "Let your coding agent install it")
	// The manual per-client route stays one click away, and stays the only
	// place any client name is claimed.
	mustContain(t, body, `id="agents"`)
	mustContain(t, body, `href="#agents"`)
}

// The JavaScript path has one prompt source in the dialog. The only duplicate
// is the explicit no-JavaScript fallback, where it must remain selectable.
func TestAgentInstallPromptIsVisibleAndCopiedFromOneSource(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()

	want := html.EscapeString(llmPrompt("en", "https://codesamplex.dev"))
	if strings.Count(body, want) != 2 {
		t.Fatalf("prompt should appear exactly twice (dialog + noscript), got %d",
			strings.Count(body, want))
	}
	mustContain(t, body, `<dialog id="install-llm-dialog"`)
	mustContain(t, body, `aria-labelledby="llm-dialog-title"`)
	mustContain(t, body, `aria-describedby="llm-dialog-body"`)
	mustContain(t, body, `<form method="dialog">`)
	mustContain(t, body, `<pre id="llm-install-prompt" class="llmtext mono" tabindex="0">`)
	mustContain(t, body, `data-copy-target="#llm-install-prompt"`)
	if strings.Contains(body, `data-copy="`+want) {
		t.Error("the agent prompt is duplicated inside a data-copy attribute")
	}
	// Copy feedback reaches screen readers, and final clipboard denial also
	// has visible localized text beside the button.
	mustContain(t, body, `data-copied="Copied"`)
	mustContain(t, body, `data-copy-failed="Copy failed. Select the prompt and copy it manually."`)
	mustContain(t, body, `class="copy-status" role="status" aria-live="polite"`)
	mustContain(t, body, `status.textContent=b.dataset.copyFailed`)
	mustContain(t, body, `aria-live="polite"`)
	// Clipboard permissions can be denied even on HTTPS (embedded browsers,
	// unfocused tabs). The visible button must retain the old selection-based
	// fallback instead of silently doing nothing.
	mustContain(t, body, `document.execCommand('copy')`)
	mustContain(t, body, `selectCopyTarget(target)`)
	mustContain(t, body, `dialog.showModal()`)
	mustContain(t, body, `dialogReturnFocus.focus()`)
	mustContain(t, body, `<noscript>`)
	// The primary action comes before the long prompt. A reader arriving at
	// the dialog must not have to scan or scroll to discover the copy button.
	button := strings.Index(body, `class="copy llmcopy mono"`)
	prompt := strings.Index(body, `<pre id="llm-install-prompt"`)
	if button < 0 || prompt < 0 || button > prompt {
		t.Errorf("copy action must precede prompt: button=%d prompt=%d", button, prompt)
	}
}

// The cache-busting token has to move with the deployed build, or a visitor
// keeps the previous release's stylesheet after a rollout. It is the short
// revision now, because that is the one part of the identity that is unique
// to a deployment: two builds of the same release line share a version.
func TestStylesheetURLTracksTheRunningBuild(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()
	mustContain(t, body, `href="/static/site.css?v=`+testBuild().ShortRevision()+`"`)
}

// An unstamped build has no token at all: a constant one is worse than none,
// because it pins every visitor to whatever they cached first.
func TestStylesheetHasNoTokenWithoutABuild(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Build = buildinfo.Info{} })
	body := get(t, mux, "/").Body.String()
	mustContain(t, body, `href="/static/site.css"`)
}

// promptURL finds every URL the prompt hands to an agent.
var promptURL = regexp.MustCompile(`https://[^\s,)）、]+`)

// A prompt is an instruction a stranger pastes into an agent with file and
// shell access. Every locale's copy must therefore point at CodeSampleX and
// nowhere else: a third-party host reachable from this box would be a
// supply-chain step that the page itself advertised.
func TestAgentInstallPromptOnlyEverPointsAtCodeSampleX(t *testing.T) {
	const base = "https://csx.example"
	allowed := map[string]bool{"github.com": true, "csx.example": true}
	for _, lang := range i18n.Supported {
		prompts := []string{llmPrompt(lang, base)}
		for _, prompt := range prompts {
			if prompt == "" {
				t.Fatalf("locale %s has an empty install prompt", lang)
			}
			// A stray percent sign in a translation would render as %!x(MISSING)
			// or leave the placeholder unfilled — either way the agent is handed
			// a URL that does not resolve.
			if strings.Contains(prompt, "%!") || strings.Contains(prompt, "%s") {
				t.Errorf("locale %s: prompt has a broken format placeholder:\n%s", lang, prompt)
			}
			for _, raw := range promptURL.FindAllString(prompt, -1) {
				u, err := url.Parse(strings.TrimRight(raw, ".,;:"))
				if err != nil {
					t.Errorf("locale %s: unparseable URL %q", lang, raw)
					continue
				}
				if !allowed[u.Host] {
					t.Errorf("locale %s: prompt sends the agent to %q", lang, u.Host)
				}
			}
		}
	}
}

// Nine locales, nine real translations. A page that switches language and
// then hands the reader an English paragraph is the one place the site
// still reads as half-finished.
func TestAgentInstallSectionIsTranslatedInEveryLocale(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	en := llmPrompt("en", "https://codesamplex.dev")
	for _, lang := range i18n.Supported {
		path := "/"
		if lang != i18n.Default {
			path = "/" + lang + "/"
		}
		body := get(t, mux, path).Body.String()
		for _, key := range []string{
			"landing.llm_heading", "landing.llm_body", "landing.llm_copy",
			"landing.llm_copied", "landing.llm_copy_failed", "landing.llm_close",
			"landing.llm_safety", "landing.llm_clients",
		} {
			val := i18n.T(lang, key)
			if strings.TrimSpace(val) == "" {
				t.Errorf("locale %s: %s is empty", lang, key)
				continue
			}
			if !strings.Contains(body, html.EscapeString(val)) {
				t.Errorf("locale %s: page does not render %s (%q)", lang, key, val)
			}
		}
		prompt := llmPrompt(lang, "https://codesamplex.dev")
		if lang != i18n.Default && prompt == en {
			t.Errorf("locale %s falls back to the English prompt", lang)
		}
		if !strings.Contains(body, html.EscapeString(prompt)) {
			t.Errorf("locale %s: prompt not rendered on %s", lang, path)
		}
	}
}

// The Korean page is the one this landed for first, and its heading is
// fixed copy rather than a translator's choice.
func TestKoreanAgentInstallHeading(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/ko/").Body.String()
	mustContain(t, body, "LLM에게 설치 맡기기")
	// The prompt must not carry the anchor page's language into the URLs:
	// /install.sh is not localized.
	mustContain(t, body, "https://codesamplex.dev/install.sh")
}
