package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

type cacheEntry struct {
	status    string
	checkedAt time.Time
}

type fakeCache struct {
	entries map[string]cacheEntry
	sets    map[string]string
}

func newFakeCache() *fakeCache {
	return &fakeCache{entries: map[string]cacheEntry{}, sets: map[string]string{}}
}

func (f *fakeCache) GetPublicness(_ context.Context, purl string) (string, time.Time, bool) {
	e, ok := f.entries[purl]
	return e.status, e.checkedAt, ok
}

func (f *fakeCache) SetPublicness(_ context.Context, purl, status string) error {
	f.sets[purl] = status
	f.entries[purl] = cacheEntry{status: status, checkedAt: time.Now()}
	return nil
}

// countingServer returns an httptest server that records every request URI
// and replies with the given status code.
func countingServer(t *testing.T, code int) (*httptest.Server, *[]string) {
	t.Helper()
	var uris []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uris = append(uris, r.RequestURI)
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv, &uris
}

func allBases(url string) map[string]string {
	return map[string]string{
		"npm": url, "pypi": url, "cargo": url, "golang": url,
		"gem": url, "composer": url, "hex": url, "pub": url, "maven": url,
	}
}

func TestCheckURLFormsAndPublic(t *testing.T) {
	tests := []struct {
		name string
		purl domain.PURL
		want string
	}{
		{"npm plain", domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}, "/axios/1.12.0"},
		{"npm scoped", domain.PURL{Ecosystem: "npm", Name: "@scope/pkg", Version: "2.0.0"}, "/@scope%2Fpkg/2.0.0"},
		{"pypi", domain.PURL{Ecosystem: "pypi", Name: "requests", Version: "2.31.0"}, "/pypi/requests/2.31.0/json"},
		{"cargo", domain.PURL{Ecosystem: "cargo", Name: "serde", Version: "1.0.0"}, "/api/v1/crates/serde/1.0.0"},
		{"golang bang escape", domain.PURL{Ecosystem: "golang", Name: "github.com/Azure/x", Version: "v1.0.0"}, "/github.com/!azure/x/@v/v1.0.0.info"},
		{"golang plain", domain.PURL{Ecosystem: "golang", Name: "golang.org/x/sys", Version: "v0.1.0"}, "/golang.org/x/sys/@v/v0.1.0.info"},
		{"gem", domain.PURL{Ecosystem: "gem", Name: "rack", Version: "3.2.4"}, "/downloads/rack-3.2.4.gem"},
		{"hex", domain.PURL{Ecosystem: "hex", Name: "req", Version: "0.5.15"}, "/tarballs/req-0.5.15.tar"},
		{"pub", domain.PURL{Ecosystem: "pub", Name: "http", Version: "1.5.0"}, "/api/archives/http-1.5.0.tar.gz"},
		{"maven", domain.PURL{Ecosystem: "maven", Name: "org.apache.commons/commons-lang3", Version: "3.17.0"}, "/org/apache/commons/commons-lang3/3.17.0/commons-lang3-3.17.0.pom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, uris := countingServer(t, http.StatusOK)
			c := &Checker{BaseURLs: allBases(srv.URL)}
			got := c.Check(context.Background(), tt.purl)
			if got != scanner.PublicnessPublic {
				t.Fatalf("Check = %q, want PUBLIC", got)
			}
			if len(*uris) != 1 || (*uris)[0] != tt.want {
				t.Fatalf("request URIs = %v, want [%s]", *uris, tt.want)
			}
		})
	}
}

func TestArchiveVersionProbesUseHEAD(t *testing.T) {
	for _, tc := range []struct {
		ecosystem string
		name      string
		version   string
	}{
		{"gem", "rack", "3.2.4"},
		{"hex", "req", "0.5.15"},
		{"pub", "http", "1.5.0"},
	} {
		t.Run(tc.ecosystem, func(t *testing.T) {
			var method string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method = r.Method
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(srv.Close)
			c := &Checker{BaseURLs: map[string]string{tc.ecosystem: srv.URL}}
			got := c.Check(context.Background(), domain.PURL{
				Ecosystem: tc.ecosystem,
				Name:      tc.name,
				Version:   tc.version,
			})
			if got != scanner.PublicnessPublic {
				t.Fatalf("Check = %q, want PUBLIC", got)
			}
			if method != http.MethodHead {
				t.Fatalf("method = %q, want HEAD", method)
			}
		})
	}
}

func TestComposerURLAndExactVersion(t *testing.T) {
	var method, uri string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, uri = r.Method, r.RequestURI
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"packages":{"guzzlehttp/guzzle":[{"version":"7.10.0"}]}}`))
	}))
	t.Cleanup(srv.Close)
	c := &Checker{BaseURLs: map[string]string{"composer": srv.URL}}
	p := domain.PURL{Ecosystem: "composer", Name: "guzzlehttp/guzzle", Version: "7.10.0"}
	if got := c.Check(context.Background(), p); got != scanner.PublicnessPublic {
		t.Fatalf("Check = %q, want PUBLIC", got)
	}
	if method != http.MethodGet || uri != "/p2/guzzlehttp/guzzle.json" {
		t.Fatalf("request = %s %s, want GET /p2/guzzlehttp/guzzle.json", method, uri)
	}
}

func TestCheckNotFoundIsPrivate(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusGone} {
		srv, _ := countingServer(t, code)
		c := &Checker{BaseURLs: allBases(srv.URL)}
		p := domain.PURL{Ecosystem: "npm", Name: "ghost", Version: "1.0.0"}
		if got := c.Check(context.Background(), p); got != scanner.PublicnessPrivate {
			t.Fatalf("status %d: Check = %q, want PRIVATE", code, got)
		}
	}
}

func TestCheckServerErrorIsUnknown(t *testing.T) {
	srv, _ := countingServer(t, http.StatusInternalServerError)
	c := &Checker{BaseURLs: allBases(srv.URL)}
	p := domain.PURL{Ecosystem: "cargo", Name: "serde", Version: "1.0.0"}
	if got := c.Check(context.Background(), p); got != scanner.PublicnessUnknown {
		t.Fatalf("Check = %q, want UNKNOWN", got)
	}
}

func TestCheckNetworkErrorIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	c := &Checker{BaseURLs: allBases(url)}
	p := domain.PURL{Ecosystem: "pypi", Name: "requests", Version: "2.31.0"}
	if got := c.Check(context.Background(), p); got != scanner.PublicnessUnknown {
		t.Fatalf("Check = %q, want UNKNOWN", got)
	}
}

func TestCheckUnknownEcosystemIsUnknown(t *testing.T) {
	c := &Checker{}
	p := domain.PURL{Ecosystem: "nuget", Name: "Newtonsoft.Json", Version: "13.0.3"}
	if got := c.Check(context.Background(), p); got != scanner.PublicnessUnknown {
		t.Fatalf("Check = %q, want UNKNOWN", got)
	}
}

func TestUnsafeOrWrongShapePackageNamesFailClosedBeforeHTTP(t *testing.T) {
	srv, uris := countingServer(t, http.StatusOK)
	c := &Checker{BaseURLs: allBases(srv.URL)}
	tests := []domain.PURL{
		{Ecosystem: "npm", Name: "react?shadow", Version: "18.2.0"},
		{Ecosystem: "npm", Name: "react#shadow", Version: "18.2.0"},
		{Ecosystem: "npm", Name: "scope/pkg", Version: "1.0.0"},
		{Ecosystem: "npm", Name: "@scope/pkg/extra", Version: "1.0.0"},
		{Ecosystem: "golang", Name: "github.com/google/uuid?shadow", Version: "v1.6.0"},
		{Ecosystem: "golang", Name: "github.com/google/uuid/../shadow", Version: "v1.6.0"},
		{Ecosystem: "pypi", Name: "requests/other", Version: "2.31.0"},
		{Ecosystem: "composer", Name: "vendor/pkg/extra", Version: "1.0.0"},
		{Ecosystem: "maven", Name: "org.example/artifact/extra", Version: "1.0.0"},
	}
	for _, p := range tests {
		if got := c.Check(context.Background(), p); got != scanner.PublicnessUnknown {
			t.Errorf("Check(%s) = %q, want UNKNOWN", p.String(), got)
		}
	}
	if len(*uris) != 0 {
		t.Fatalf("invalid names reached registry HTTP: %v", *uris)
	}
}

func TestValidNestedRegistryNamesRemainAccepted(t *testing.T) {
	for _, tc := range []struct {
		ecosystem string
		name      string
	}{
		{"npm", "@modelcontextprotocol/sdk"},
		{"golang", "github.com/Azure/azure-sdk-for-go/sdk/azcore"},
		{"composer", "guzzlehttp/guzzle"},
		{"maven", "org.apache.commons/commons-lang3"},
	} {
		if !ValidPackageName(tc.ecosystem, tc.name) {
			t.Errorf("ValidPackageName(%q, %q) = false", tc.ecosystem, tc.name)
		}
	}
}

func TestCacheHitAvoidsSecondRequest(t *testing.T) {
	srv, uris := countingServer(t, http.StatusOK)
	cache := newFakeCache()
	c := &Checker{Cache: cache, BaseURLs: allBases(srv.URL)}
	p := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}

	if got := c.Check(context.Background(), p); got != scanner.PublicnessPublic {
		t.Fatalf("first Check = %q, want PUBLIC", got)
	}
	if got := c.Check(context.Background(), p); got != scanner.PublicnessPublic {
		t.Fatalf("second Check = %q, want PUBLIC", got)
	}
	if len(*uris) != 1 {
		t.Fatalf("HTTP requests = %d, want 1 (cache hit must skip HTTP)", len(*uris))
	}
	if cache.sets[p.String()] != scanner.PublicnessPublic {
		t.Fatalf("cache sets = %v, want PUBLIC stored under %s", cache.sets, p.String())
	}
}

func TestExpiredCacheEntryRefetches(t *testing.T) {
	srv, uris := countingServer(t, http.StatusOK)
	cache := newFakeCache()
	p := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	cache.entries[p.String()] = cacheEntry{status: scanner.PublicnessPrivate, checkedAt: time.Now().Add(-25 * time.Hour)}

	c := &Checker{Cache: cache, BaseURLs: allBases(srv.URL)}
	if got := c.Check(context.Background(), p); got != scanner.PublicnessPublic {
		t.Fatalf("Check = %q, want PUBLIC after expiry", got)
	}
	if len(*uris) != 1 {
		t.Fatalf("HTTP requests = %d, want 1 (expired entry must refetch)", len(*uris))
	}
}

func TestUnknownResultIsNotCached(t *testing.T) {
	srv, _ := countingServer(t, http.StatusInternalServerError)
	cache := newFakeCache()
	c := &Checker{Cache: cache, BaseURLs: allBases(srv.URL)}
	p := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	if got := c.Check(context.Background(), p); got != scanner.PublicnessUnknown {
		t.Fatalf("Check = %q, want UNKNOWN", got)
	}
	if len(cache.sets) != 0 {
		t.Fatalf("cache sets = %v, want none for UNKNOWN result", cache.sets)
	}
}

func TestCheckAllUpgradesOnlyUnknown(t *testing.T) {
	srv, uris := countingServer(t, http.StatusOK)
	c := &Checker{BaseURLs: allBases(srv.URL)}

	pkgs := []scanner.ResolvedPackage{
		{PURL: domain.PURL{Ecosystem: "npm", Name: "local-lib", Version: "0.0.1"}, Publicness: scanner.PublicnessPrivate},
		{PURL: domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}, Publicness: scanner.PublicnessUnknown},
		{PURL: domain.PURL{Ecosystem: "cargo", Name: "serde", Version: "1.0.0"}, Publicness: scanner.PublicnessPublic},
	}
	c.CheckAll(context.Background(), pkgs)

	if pkgs[0].Publicness != scanner.PublicnessPrivate {
		t.Fatalf("private pkg publicness = %q, want PRIVATE untouched", pkgs[0].Publicness)
	}
	if pkgs[1].Publicness != scanner.PublicnessPublic {
		t.Fatalf("unknown pkg publicness = %q, want upgraded to PUBLIC", pkgs[1].Publicness)
	}
	if pkgs[2].Publicness != scanner.PublicnessPublic {
		t.Fatalf("public pkg publicness = %q, want PUBLIC untouched", pkgs[2].Publicness)
	}
	if len(*uris) != 1 || (*uris)[0] != "/axios/1.12.0" {
		t.Fatalf("request URIs = %v, want only [/axios/1.12.0] (PRIVATE and PUBLIC never queried)", *uris)
	}
}

func TestDefaultBaseURLs(t *testing.T) {
	c := &Checker{}
	want := map[string]string{
		"npm":      "https://registry.npmjs.org/axios",
		"pypi":     "https://pypi.org/pypi/x/json",
		"cargo":    "https://crates.io/api/v1/crates/x",
		"golang":   "https://proxy.golang.org/example.com/x/@latest",
		"gem":      "https://rubygems.org/api/v1/versions/x.json",
		"composer": "https://repo.packagist.org/p2/vendor/x.json",
		"hex":      "https://hex.pm/api/packages/x",
		"pub":      "https://pub.dev/api/packages/x",
	}
	for eco, wantURL := range want {
		name := "x"
		if eco == "npm" {
			name = "axios"
		} else if eco == "golang" {
			name = "example.com/x"
		} else if eco == "composer" {
			name = "vendor/x"
		}
		got := c.checkURL(domain.PURL{Ecosystem: eco, Name: name, Version: "1"})
		if got != wantURL {
			t.Errorf("checkURL(%s) = %q, want %q", eco, got, wantURL)
		}
	}
}

func TestDefaultExactVersionURLs(t *testing.T) {
	c := &Checker{}
	for _, tc := range []struct {
		purl domain.PURL
		want string
	}{
		{domain.PURL{Ecosystem: "gem", Name: "rack", Version: "3.2.4"}, "https://rubygems.org/downloads/rack-3.2.4.gem"},
		{domain.PURL{Ecosystem: "composer", Name: "guzzlehttp/guzzle", Version: "7.10.0"}, "https://repo.packagist.org/p2/guzzlehttp/guzzle.json"},
		{domain.PURL{Ecosystem: "hex", Name: "req", Version: "0.5.15"}, "https://repo.hex.pm/tarballs/req-0.5.15.tar"},
		{domain.PURL{Ecosystem: "pub", Name: "http", Version: "1.5.0"}, "https://pub.dev/api/archives/http-1.5.0.tar.gz"},
	} {
		if got := c.versionURL(tc.purl); got != tc.want {
			t.Errorf("versionURL(%s) = %q, want %q", tc.purl.String(), got, tc.want)
		}
	}
}
