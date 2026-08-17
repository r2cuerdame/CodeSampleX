package web

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// The install box asks a visitor to pipe a stranger's script into a shell.
// The alternative — hand the same install to the agent already open in
// their editor — is only an alternative if it is reachable from the anchor
// every "Install" link on the site points at.
func TestInstallSectionOffersTheAgentDrivenPath(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()

	mustContain(t, body, `href="#install-llm"`)
	mustContain(t, body, `id="install-llm"`)
	mustContain(t, body, "Let your coding agent install it")
	// The manual per-client route stays one click away, and stays the only
	// place any client name is claimed.
	mustContain(t, body, `id="agents"`)
	mustContain(t, body, `href="#agents"`)
}

// The prompt a reader checks must be the prompt their agent receives. It is
// rendered twice — visibly, and inside the copy button — from one field, so
// this is what catches the two drifting apart.
func TestAgentInstallPromptIsVisibleAndCopiedFromOneSource(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()

	want := html.EscapeString(llmPrompt("en", "https://codesamplex.dev"))
	if strings.Count(body, want) != 2 {
		t.Fatalf("prompt should appear exactly twice (visible text + data-copy), got %d",
			strings.Count(body, want))
	}
	mustContain(t, body, `<pre class="llmtext mono">`)
	mustContain(t, body, `data-copy="`)
	// Copy feedback that a screen reader receives, not just a green border.
	mustContain(t, body, `data-copied="Copied"`)
	mustContain(t, body, `aria-live="polite"`)
	// No JavaScript: the prompt is still on the page, in full, selectable.
	if !strings.Contains(body, ">"+want+"</pre>") {
		t.Error("prompt is not rendered as readable page text")
	}
	// The primary action comes before the long prompt. A reader arriving at
	// the anchor must not have to scan or scroll to discover the copy button.
	button := strings.Index(body, `class="copy llmcopy mono"`)
	prompt := strings.Index(body, `<pre class="llmtext mono">`)
	if button < 0 || prompt < 0 || button > prompt {
		t.Errorf("copy action must precede prompt: button=%d prompt=%d", button, prompt)
	}
}

func TestWorkerOnlyPromptIsVisibleCopiedAndIsolated(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/contribute").Body.String()

	want := html.EscapeString(workerPrompt("en", "https://codesamplex.dev"))
	if strings.Count(body, want) != 2 {
		t.Fatalf("worker prompt should appear exactly twice (visible text + data-copy), got %d",
			strings.Count(body, want))
	}
	for _, required := range []string{
		`id="install-worker"`,
		"Worker-only machine",
		"Copy worker prompt",
		"csx init --community --yes --no-agents --no-daemon",
		"csx worker start --mode verify --parallel 2 --budget idle",
		"Docker",
	} {
		mustContain(t, body, required)
	}
	if strings.Contains(workerPrompt("en", "https://codesamplex.dev"), "mcp-config") {
		t.Error("worker-only prompt must not configure an MCP client")
	}
}

func TestStylesheetURLTracksTheRunningVersion(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()
	mustContain(t, body, `href="/static/site.css?v=1.0.0-test"`)
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
		prompts := []string{llmPrompt(lang, base), workerPrompt(lang, base)}
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
		for _, want := range []string{
			base + "/install.sh",
			base + "/install.ps1",
			"github.com/r2cuerdame/CodeSampleX/blob/main/llms-install.md",
			"search_known_solution",
			"csx sync",
			"csx init --community --yes",
			"mode: community",
		} {
			if !strings.Contains(llmPrompt(lang, base), want) {
				t.Errorf("locale %s: prompt is missing %q", lang, want)
			}
		}
		for _, want := range []string{
			base + "/install.sh",
			base + "/install.ps1",
			"github.com/r2cuerdame/CodeSampleX/blob/main/llms-install.md",
			"csx init --community --yes --no-agents --no-daemon",
			"csx worker start --mode verify --parallel 2 --budget idle",
			"Docker",
		} {
			if !strings.Contains(workerPrompt(lang, base), want) {
				t.Errorf("locale %s: worker prompt is missing %q", lang, want)
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
			"landing.llm_copied", "landing.llm_safety", "landing.llm_clients",
			"landing.llm_link",
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
