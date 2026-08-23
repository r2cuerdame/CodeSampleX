package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README publishes an API table: "the same data the website renders, as
// JSON, without an account". A reader copies a path out of it and calls it.
//
// Nothing kept that table equal to the router. A route renamed or dropped
// leaves a documented endpoint answering 404, and a reader has no way to tell
// that from an outage. This test asks the mux itself whether each path the
// README publishes still resolves — not whether some string appears in some
// source file, but whether the server would route it.

// docRoute is one row of the README's API table.
type docRoute struct {
	method string
	path   string
	// probe is the path with the {placeholders} filled in, because a
	// ServeMux resolves concrete paths, not patterns.
	probe string
}

var readmeAPITablePattern = regexp.MustCompile(
	"`(GET|POST) (/v[0-9]+/[^`]*)`")

// placeholders maps a documented path segment template to something the mux
// will actually match. The value is arbitrary — only the shape matters.
var placeholders = strings.NewReplacer(
	"{purl}", "pkg%3Anpm%2Faxios%401.12.0",
	"{eco}", "npm",
	"{ecosystem}", "npm",
	"{package}", "axios",
	"{name}", "axios",
	"{family}", "axios.post",
	"{symbol}", "axios.post",
	"{major}", "1",
	"{n}", "1",
	"{id}", "sha256%3Aabc",
	"{sampleId}", "sha256%3Aabc",
)

func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// readmeAPIRoutes reads the API table out of the README rather than being
// handed a copy of it. A copy is one more thing that can go stale, and the
// staleness of copies is the whole reason this file exists.
func readmeAPIRoutes(t *testing.T) []docRoute {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	i := strings.Index(body, "## API")
	if i < 0 {
		t.Fatal("README.md has no API section")
	}
	section := body[i:]
	if j := strings.Index(section[len("## API"):], "\n## "); j >= 0 {
		section = section[:len("## API")+j]
	}

	var routes []docRoute
	seen := map[string]bool{}
	for _, m := range readmeAPITablePattern.FindAllStringSubmatch(section, -1) {
		method, path := m[1], strings.TrimSuffix(m[2], "/")
		// The table abbreviates a second route on one row as "…/artifact".
		if strings.Contains(path, "…") {
			continue
		}
		key := method + " " + path
		if seen[key] {
			continue
		}
		seen[key] = true
		routes = append(routes, docRoute{method, path, placeholders.Replace(path)})
	}
	if len(routes) < 5 {
		t.Fatalf("only %d routes parsed out of the README API table; the table shape changed", len(routes))
	}
	return routes
}

func TestEveryRouteTheREADMEPublishesIsRegistered(t *testing.T) {
	mux := NewMux(Deps{})
	routes := readmeAPIRoutes(t)
	t.Logf("README publishes %d read routes", len(routes))
	for _, r := range routes {
		req := httptest.NewRequest(r.method, r.probe, nil)
		_, pattern := mux.Handler(req)
		if pattern == "" {
			t.Errorf("README.md publishes %s %s, which the router does not register (probed %s)",
				r.method, r.path, r.probe)
		}
	}
}

// The other half of the same contract: a route the README publishes must not
// need credentials. The write half — evidence batches, sample publication,
// verification jobs, the device-code flow — needs a seeder identity or a
// worker token, and publishing it as "without an account" invites requests
// that can only be refused.
func TestTheREADMEDoesNotPublishTheCredentialedHalf(t *testing.T) {
	for _, r := range readmeAPIRoutes(t) {
		for _, gated := range []string{
			"/v1/evidence/batches", "/v1/authoring/", "/v1/auth/github/",
			"/v1/verification/jobs", "/v1/adoptions", "/v1/verifications",
		} {
			if strings.HasPrefix(r.path, gated) {
				t.Errorf("README.md lists %s %s under an API it calls account-free; it is not",
					r.method, r.path)
			}
		}
	}
}

// A sanity check on the probe itself: a path the router has never heard of
// must come back unregistered, or the test above would pass on anything.
func TestTheRouteProbeCanFail(t *testing.T) {
	mux := NewMux(Deps{})
	req := httptest.NewRequest(http.MethodGet, "/v1/definitely-not-a-route", nil)
	if _, pattern := mux.Handler(req); pattern != "" {
		t.Fatalf("an invented path resolved to %q; the probe proves nothing", pattern)
	}
}
