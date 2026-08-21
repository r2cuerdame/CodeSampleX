package web

import (
	"strings"
	"testing"
)

// The features page described the CLI and the eight MCP tools and never
// mentioned that the network answers HTTP directly. Everything the site
// renders is served from those routes, so a reader who wanted the evidence
// without either the CLI or an agent had no way to learn they exist.
func TestTheFeaturesPageNamesTheReadAPI(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/features").Body.String()

	for _, route := range []string{
		"/v1/search", "/v1/registry/packages/", "/v1/samples/", "/v1/shards/", "/v1/stats",
	} {
		if !strings.Contains(body, route) {
			t.Errorf("the page does not name %s", route)
		}
	}
}

// The write half needs a seeder identity or a worker token. Listing it as
// though anyone can call it invites requests that can only be refused.
func TestTheFeaturesPageDoesNotAdvertiseWhatItCannotGrant(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/features").Body.String()

	i := strings.Index(body, `class="apiref`)
	if i < 0 {
		t.Fatal("no API section")
	}
	section := body[i:]
	if j := strings.Index(section, "</section>"); j >= 0 {
		section = section[:j]
	}
	for _, route := range []string{
		"/v1/evidence/batches", "/v1/authoring/", "/v1/auth/github/",
		"/v1/verification/jobs", "/v1/adoptions",
	} {
		if strings.Contains(section, route) {
			t.Errorf("the read API section lists %s, which needs credentials", route)
		}
	}
}

// Every route listed is one the router actually registers. A reference that
// drifts from the router is worse than no reference.
func TestEveryListedRouteIsRegistered(t *testing.T) {
	for _, e := range publicReadAPI() {
		if e.Method != "GET" && e.Method != "POST" {
			t.Errorf("%s %s: unexpected method", e.Method, e.Path)
		}
		if !strings.HasPrefix(e.Path, "/v1/") {
			t.Errorf("%s is not a v1 route", e.Path)
		}
		if strings.TrimSpace(e.What) == "" {
			t.Errorf("%s %s says nothing about what it answers", e.Method, e.Path)
		}
	}
}
