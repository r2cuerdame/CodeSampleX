// Package registry checks whether packages exist on their public registry.
// Automatic evidence collection is allowed only for PUBLIC packages
// (goal.md §8.1); any failure to determine publicness yields UNKNOWN,
// which downstream code treats as private — the safe default.
package registry

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

const (
	cacheTTL       = 24 * time.Hour
	requestTimeout = 5 * time.Second
)

// Cache stores publicness verdicts keyed by canonical purl string.
// Implementations are expected to be backed by localdb.
type Cache interface {
	GetPublicness(ctx context.Context, purl string) (status string, checkedAt time.Time, ok bool)
	SetPublicness(ctx context.Context, purl, status string) error
}

var defaultBaseURLs = map[string]string{
	"npm":    "https://registry.npmjs.org",
	"pypi":   "https://pypi.org",
	"cargo":  "https://crates.io",
	"golang": "https://proxy.golang.org",
}

var defaultClient = &http.Client{Timeout: requestTimeout}

// Checker resolves package publicness against the four Public v1 registries.
// The zero value uses the real registry endpoints, a 5s-timeout HTTP client,
// and no cache.
type Checker struct {
	Cache    Cache
	HTTP     *http.Client
	BaseURLs map[string]string // ecosystem → base URL override, mainly for tests
}

// Check reports PUBLIC, PRIVATE, or UNKNOWN for p. It never blocks longer
// than the request timeout and never returns an error: any transport or
// server failure is UNKNOWN. Fresh cache entries (younger than 24h) are
// returned without a network call; UNKNOWN verdicts are never cached so the
// next call retries.
func (c *Checker) Check(ctx context.Context, p domain.PURL) string {
	key := p.String()
	if c.Cache != nil {
		if status, at, ok := c.Cache.GetPublicness(ctx, key); ok && time.Since(at) < cacheTTL {
			return status
		}
	}
	status := c.probe(ctx, p)
	if status != scanner.PublicnessUnknown && c.Cache != nil {
		c.Cache.SetPublicness(ctx, key, status)
	}
	return status
}

// probe asks the registry about the exact package the caller named, version
// included.
//
// The verdict is cached under the full purl — name AND version — but every
// probe was by name alone. So pkg:npm/react@99.0.0 asked the registry about
// react, got a 200 for the package, and was filed as PUBLIC for a release
// that has never existed. Publicness is the gate on what may leave a machine
// and what the server may ingest, so any version string of the caller's
// choosing entered the network under a real package's name — and shards are
// keyed by major, so it landed in a shard of its own that nothing could ever
// verify.
//
// A version the registry does not serve is never PUBLIC. It is PRIVATE only
// when the package itself is absent, which is what PRIVATE has always meant;
// when the package is public and the version is not, nothing is established
// either way, so the answer is UNKNOWN — uncached, retried next time, and
// treated as private everywhere downstream.
func (c *Checker) probe(ctx context.Context, p domain.PURL) string {
	if p.Version == "" {
		return c.nameStatus(ctx, p)
	}
	u := c.versionURL(p)
	if u == "" {
		return c.nameStatus(ctx, p)
	}
	switch c.get(ctx, u) {
	case http.StatusOK:
		return scanner.PublicnessPublic
	case http.StatusNotFound, http.StatusGone:
		// Tell "no such package" apart from "no such version": only the
		// first is this package being private.
		if c.nameStatus(ctx, p) == scanner.PublicnessPrivate {
			return scanner.PublicnessPrivate
		}
		return scanner.PublicnessUnknown
	}
	return scanner.PublicnessUnknown
}

// nameStatus is the package-level existence probe: does this registry serve
// anything by this name at all?
func (c *Checker) nameStatus(ctx context.Context, p domain.PURL) string {
	u := c.checkURL(p)
	if u == "" {
		return scanner.PublicnessUnknown
	}
	switch c.get(ctx, u) {
	case http.StatusOK:
		return scanner.PublicnessPublic
	case http.StatusNotFound, http.StatusGone:
		return scanner.PublicnessPrivate
	}
	return scanner.PublicnessUnknown
}

// get returns the status code, or 0 when the request never completed.
func (c *Checker) get(ctx context.Context, u string) int {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return 0
	}
	client := c.HTTP
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// CheckAll upgrades the Publicness of UNKNOWN entries in place. PRIVATE
// entries are never queried (goal.md §25.E) and PUBLIC entries are left
// as-is.
func (c *Checker) CheckAll(ctx context.Context, pkgs []scanner.ResolvedPackage) {
	for i := range pkgs {
		if pkgs[i].Publicness != scanner.PublicnessUnknown {
			continue
		}
		pkgs[i].Publicness = c.Check(ctx, pkgs[i].PURL)
	}
}

// checkURL builds the per-ecosystem existence-probe URL, or "" for an
// ecosystem outside the Public v1 allowlist.
func (c *Checker) checkURL(p domain.PURL) string {
	base, ok := c.BaseURLs[p.Ecosystem]
	if !ok {
		if base, ok = defaultBaseURLs[p.Ecosystem]; !ok {
			return ""
		}
	}
	base = strings.TrimSuffix(base, "/")
	switch p.Ecosystem {
	case "npm":
		name := p.Name
		if scoped, rest, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scoped, "@") {
			name = scoped + "%2F" + rest
		}
		return base + "/" + name
	case "pypi":
		return base + "/pypi/" + url.PathEscape(p.Name) + "/json"
	case "cargo":
		return base + "/api/v1/crates/" + url.PathEscape(p.Name)
	case "golang":
		return base + "/" + escapeGoModule(p.Name) + "/@latest"
	}
	return ""
}

// versionURL builds the probe for one specific release, or "" when the
// ecosystem is outside the Public v1 allowlist. Every one of the four
// registries serves a per-version endpoint, so a version claim is always
// checkable against the registry that would have to have published it.
func (c *Checker) versionURL(p domain.PURL) string {
	pkg := c.checkURL(p)
	if pkg == "" || p.Version == "" {
		return ""
	}
	switch p.Ecosystem {
	case "npm":
		return pkg + "/" + url.PathEscape(p.Version)
	case "pypi":
		// checkURL ends in /json; the version goes before it.
		return strings.TrimSuffix(pkg, "/json") + "/" + url.PathEscape(p.Version) + "/json"
	case "cargo":
		return pkg + "/" + url.PathEscape(p.Version)
	case "golang":
		// The proxy applies the same case encoding to versions as to module
		// paths, and wants the v prefix a go.mod always carries.
		v := p.Version
		if !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		return strings.TrimSuffix(pkg, "/@latest") + "/@v/" + escapeGoModule(v) + ".info"
	}
	return ""
}

// escapeGoModule applies Go module proxy case encoding: every uppercase
// letter becomes '!' followed by its lowercase form (Azure → !azure).
func escapeGoModule(path string) string {
	if strings.IndexFunc(path, func(r rune) bool { return 'A' <= r && r <= 'Z' }) < 0 {
		return path
	}
	var b strings.Builder
	b.Grow(len(path) + 4)
	for _, r := range path {
		if 'A' <= r && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
