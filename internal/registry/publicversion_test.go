package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// A registry that serves the package but only one release of it.
func oneVersionRegistry(t *testing.T, real string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		// Anything under the known package name that is not the one release
		// it published is a 404, exactly as the real registries answer.
		if strings.Contains(r.URL.Path, "unheard-of") {
			http.NotFound(w, r)
			return
		}
		if v := versionSegment(r.URL.Path); v != "" && strings.TrimPrefix(v, "v") != real {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// versionSegment pulls the release out of the four probe shapes, or "" when
// the request is the package-level probe.
func versionSegment(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	n := len(parts)
	switch {
	case n == 4 && parts[0] == "pypi" && parts[3] == "json":
		return parts[2]
	case n >= 2 && parts[n-2] == "@v":
		return strings.TrimSuffix(parts[n-1], ".info")
	case n == 5 && parts[0] == "api" && parts[1] == "v1":
		return parts[4]
	case n == 2 && parts[0] != "pypi" && parts[0] != "api":
		return parts[1]
	}
	return ""
}

// Publicness decides what may leave a machine and what the server may
// ingest, and it is cached under the FULL purl — name and version. Probing
// by name alone meant pkg:npm/react@99.0.0 asked about react, got a 200,
// and was filed PUBLIC for a release that has never existed. Shards are
// keyed by major, so that lands in a shard of its own that no verifier can
// ever reach.
func TestVersionThatWasNeverPublishedIsNotPublic(t *testing.T) {
	cases := []struct {
		eco, purl string
	}{
		{"npm", "pkg:npm/react@99.0.0"},
		{"pypi", "pkg:pypi/requests@99.0.0"},
		{"cargo", "pkg:cargo/serde@99.0.0"},
		{"golang", "pkg:golang/github.com/google/uuid@v99.0.0"},
	}
	for _, tc := range cases {
		srv, _ := oneVersionRegistry(t, "1.0.0")
		c := &Checker{BaseURLs: map[string]string{tc.eco: srv.URL}}
		p, err := domain.ParsePURL(tc.purl)
		if err != nil {
			t.Fatalf("%s: %v", tc.purl, err)
		}
		if got := c.Check(context.Background(), p); got == scanner.PublicnessPublic {
			t.Errorf("%s: a version the registry never served was graded PUBLIC", tc.purl)
		}
	}
}

// The version that DID ship still has to come back PUBLIC, or the gate
// closes on the whole network.
func TestPublishedVersionStaysPublic(t *testing.T) {
	for _, tc := range []struct{ eco, purl string }{
		{"npm", "pkg:npm/react@1.0.0"},
		{"pypi", "pkg:pypi/requests@1.0.0"},
		{"cargo", "pkg:cargo/serde@1.0.0"},
		{"golang", "pkg:golang/github.com/google/uuid@v1.0.0"},
	} {
		srv, seen := oneVersionRegistry(t, "1.0.0")
		c := &Checker{BaseURLs: map[string]string{tc.eco: srv.URL}}
		p, _ := domain.ParsePURL(tc.purl)
		if got := c.Check(context.Background(), p); got != scanner.PublicnessPublic {
			t.Errorf("%s: got %s, want PUBLIC (probes: %v)", tc.purl, got, *seen)
		}
		// One request in the ordinary case: the version probe answers it.
		if len(*seen) != 1 {
			t.Errorf("%s: %d probes, want 1: %v", tc.purl, len(*seen), *seen)
		}
	}
}

func TestAdditionalEcosystemExactVersionVerdicts(t *testing.T) {
	for _, tc := range []struct {
		ecosystem   string
		published   string
		unpublished string
		absent      string
		versionPath string
		namePath    string
	}{
		{
			"gem",
			"pkg:gem/rack@1.0.0", "pkg:gem/rack@99.0.0", "pkg:gem/unheard-of@1.0.0",
			"/downloads/rack-1.0.0.gem", "/api/v1/versions/rack.json",
		},
		{
			"hex",
			"pkg:hex/req@1.0.0", "pkg:hex/req@99.0.0", "pkg:hex/unheard-of@1.0.0",
			"/tarballs/req-1.0.0.tar", "/api/packages/req",
		},
		{
			"pub",
			"pkg:pub/http@1.0.0", "pkg:pub/http@99.0.0", "pkg:pub/unheard-of@1.0.0",
			"/api/archives/http-1.0.0.tar.gz", "/api/packages/http",
		},
	} {
		t.Run(tc.ecosystem, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case tc.versionPath, tc.namePath:
					w.WriteHeader(http.StatusOK)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			c := &Checker{BaseURLs: map[string]string{tc.ecosystem: srv.URL}}
			assertPublicness(t, c, tc.published, scanner.PublicnessPublic)
			assertPublicness(t, c, tc.unpublished, scanner.PublicnessUnknown)
			assertPublicness(t, c, tc.absent, scanner.PublicnessPrivate)
		})
	}
}

func TestComposerExactVersionVerdicts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/p2/guzzlehttp/guzzle.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"packages":{"guzzlehttp/guzzle":[{"version":"1.0.0"},{"version":"1.1.0"}]}}`))
	}))
	t.Cleanup(srv.Close)
	c := &Checker{BaseURLs: map[string]string{"composer": srv.URL}}
	assertPublicness(t, c, "pkg:composer/guzzlehttp/guzzle@1.0.0", scanner.PublicnessPublic)
	assertPublicness(t, c, "pkg:composer/guzzlehttp/guzzle@99.0.0", scanner.PublicnessUnknown)
	assertPublicness(t, c, "pkg:composer/unheard-of/package@1.0.0", scanner.PublicnessPrivate)
}

func TestComposerMetadataBodyIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(strings.Repeat(" ", maxComposerMetadataBody+1)))
	}))
	t.Cleanup(srv.Close)
	c := &Checker{BaseURLs: map[string]string{"composer": srv.URL}}
	assertPublicness(t, c, "pkg:composer/guzzlehttp/guzzle@1.0.0", scanner.PublicnessUnknown)
}

func assertPublicness(t *testing.T, c *Checker, rawPURL, want string) {
	t.Helper()
	p, err := domain.ParsePURL(rawPURL)
	if err != nil {
		t.Fatalf("ParsePURL(%q): %v", rawPURL, err)
	}
	if got := c.Check(context.Background(), p); got != want {
		t.Errorf("Check(%s) = %s, want %s", rawPURL, got, want)
	}
}

// A package the registry has never heard of is PRIVATE, not merely unknown:
// that verdict is what keeps a company's internal package from being
// reported, and it must survive the version probe landing first.
func TestAbsentPackageIsStillPrivate(t *testing.T) {
	srv, _ := oneVersionRegistry(t, "1.0.0")
	c := &Checker{BaseURLs: map[string]string{"npm": srv.URL}}
	p, _ := domain.ParsePURL("pkg:npm/unheard-of@1.0.0")
	if got := c.Check(context.Background(), p); got != scanner.PublicnessPrivate {
		t.Errorf("got %s, want PRIVATE", got)
	}
}

// UNKNOWN must not be cached — it means "ask again", and a loose pin that
// the registry normalizes should get a real answer on the next run rather
// than a day-old shrug.
func TestUnknownVerdictIsNotCached(t *testing.T) {
	srv, _ := oneVersionRegistry(t, "1.0.0")
	cache := memCache{}
	c := &Checker{BaseURLs: map[string]string{"npm": srv.URL}, Cache: cache}
	p, _ := domain.ParsePURL("pkg:npm/react@2.0.0")
	if got := c.Check(context.Background(), p); got != scanner.PublicnessUnknown {
		t.Fatalf("got %s, want UNKNOWN", got)
	}
	if _, _, ok := cache.GetPublicness(context.Background(), p.String()); ok {
		t.Error("an UNKNOWN verdict was cached")
	}
}

type memCache map[string]string

func (m memCache) GetPublicness(_ context.Context, purl string) (string, time.Time, bool) {
	s, ok := m[purl]
	return s, time.Now(), ok
}

func (m memCache) SetPublicness(_ context.Context, purl, status string) error {
	m[purl] = status
	return nil
}
