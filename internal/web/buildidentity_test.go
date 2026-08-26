package web

import (
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/buildinfo"
)

// The footer is the five-second answer to "what is actually running in
// production right now". Production served
// `csx 2a6af6a8d73f51e4c941908f76527bd9899437ce`: one 40-character blob
// labelled with the name of the CLI a visitor downloads, which is a
// different artifact with a different version. It named neither the server
// build nor the environment, and a staging deployment rendered the same
// shape.
func TestFooterNamesTheRunningServerBuild(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) {
		d.Build = testBuild()
	})
	body := get(t, mux, "/").Body.String()

	foot := footerOf(t, body)
	for _, want := range []string{
		"server v0.1.44-66",
		"2a6af6a",
		"production",
	} {
		if !strings.Contains(foot, want) {
			t.Errorf("footer does not name %q:\n%s", want, foot)
		}
	}
	// The label is the running server, not the CLI release a visitor installs.
	if strings.Contains(foot, "csx v0.1.44-66") || strings.Contains(foot, "· csx ") {
		t.Errorf("footer still labels the server build as the csx client:\n%s", foot)
	}
	// The full revision belongs in the backend and in the hover detail, never
	// as forty characters of visible chrome.
	if strings.Contains(stripTitles(foot), fullTestRevision) {
		t.Errorf("footer renders the full revision as visible text:\n%s", foot)
	}
	if !strings.Contains(foot, fullTestRevision) {
		t.Errorf("footer hover detail does not carry the full revision:\n%s", foot)
	}
}

const fullTestRevision = "2a6af6a8d73f51e4c941908f76527bd9899437ce"

// testBuild is the production shape: a stamped release line, the immutable
// revision, and the deployment it belongs to.
func testBuild() buildinfo.Info {
	return buildinfo.Info{
		Version:     "v0.1.44-66",
		Revision:    fullTestRevision,
		Environment: "production",
		BuiltAt:     time.Date(2026, 8, 26, 0, 11, 2, 0, time.UTC),
	}
}

// A build nobody stamped has nothing to report, and a footer that invents
// "dev" on a host the reader believes is production is worse than silence.
func TestFooterOmitsAnUnknownBuild(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Build = buildinfo.Info{} })
	foot := footerOf(t, get(t, mux, "/").Body.String())
	if strings.Contains(foot, "server ") {
		t.Errorf("footer claims a server build with nothing to claim: %s", foot)
	}
}

// Staging must not render as production. The environment is part of the
// visible line, not only of the hover detail.
func TestFooterNamesANonProductionEnvironment(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) {
		b := testBuild()
		b.Environment = "staging"
		d.Build = b
	})
	foot := stripTitles(footerOf(t, get(t, mux, "/").Body.String()))
	if !strings.Contains(foot, "staging") {
		t.Errorf("footer does not name the staging environment: %s", foot)
	}
	if strings.Contains(foot, "production") {
		t.Errorf("a staging build renders as production: %s", foot)
	}
}

// The hover detail is localized; the identity inside it is data and is not.
func TestFooterDetailIsLocalized(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Build = testBuild() })
	foot := footerOf(t, get(t, mux, "/ko/").Body.String())
	if !strings.Contains(foot, "실행 중인 서버 빌드") {
		t.Errorf("Korean page keeps the English build label: %s", foot)
	}
	if !strings.Contains(foot, fullTestRevision) {
		t.Errorf("Korean page loses the full revision: %s", foot)
	}
}

// The csx a visitor downloads is not this server build. Publishing the
// server revision as the client's softwareVersion told every crawler it was.
func TestLandingStructuredDataDoesNotVersionTheClientAsTheServer(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Build = testBuild() })
	body := get(t, mux, "/").Body.String()
	if strings.Contains(body, "softwareVersion") {
		t.Errorf("landing JSON-LD still publishes a softwareVersion for csx")
	}
	if strings.Contains(body, fullTestRevision) && strings.Contains(body, `"SoftwareApplication"`) {
		// The revision may appear once, in the footer hover detail. It must
		// not appear inside the JSON-LD block.
		start := strings.Index(body, `"SoftwareApplication"`)
		end := strings.Index(body[start:], "</script>")
		if end > 0 && strings.Contains(body[start:start+end], fullTestRevision) {
			t.Errorf("the server revision leaked into the csx JSON-LD node")
		}
	}
}

func footerOf(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "<footer")
	end := strings.Index(body, "</footer>")
	if start < 0 || end < start {
		t.Fatalf("page has no footer")
	}
	return body[start:end]
}

// stripTitles removes every title="..." attribute so a test can ask what the
// footer shows without matching what it only reveals on hover.
func stripTitles(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, ` title="`)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		rest := s[i+len(` title="`):]
		j := strings.Index(rest, `"`)
		if j < 0 {
			return b.String()
		}
		s = rest[j+1:]
	}
}
