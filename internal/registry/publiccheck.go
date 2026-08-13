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
	u := c.checkURL(p)
	if u == "" {
		return scanner.PublicnessUnknown
	}
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return scanner.PublicnessUnknown
	}
	client := c.HTTP
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return scanner.PublicnessUnknown
	}
	resp.Body.Close()

	var status string
	switch resp.StatusCode {
	case http.StatusOK:
		status = scanner.PublicnessPublic
	case http.StatusNotFound, http.StatusGone:
		status = scanner.PublicnessPrivate
	default:
		return scanner.PublicnessUnknown
	}
	if c.Cache != nil {
		c.Cache.SetPublicness(ctx, key, status)
	}
	return status
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
