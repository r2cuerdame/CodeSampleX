// Package registry checks whether packages exist on their public registry.
// Automatic evidence collection is allowed only for PUBLIC packages
// (goal.md §8.1); any failure to determine publicness yields UNKNOWN,
// which downstream code treats as private — the safe default.
package registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

const (
	cacheTTL                = 24 * time.Hour
	requestTimeout          = 5 * time.Second
	maxComposerMetadataBody = 4 << 20
)

// userAgent identifies this client to the public registries it probes.
//
// crates.io enforces a crawler policy: a request whose User-Agent does not
// identify the caller is answered 403. Go's default "Go-http-client/1.1" is
// exactly such a request, and 403 is neither the 200 that means PUBLIC nor
// the 404 that means PRIVATE, so probe returned UNKNOWN — which is never
// cached, so every cargo coordinate was re-probed and refused forever, and
// every observation about it was rejected at ingest (#176).
//
// The other registries do not demand this, but a probe that says who is
// asking is the correct shape for all of them, so it is set once for every
// outbound request rather than per ecosystem.
const userAgent = "codesamplex/1.0 (+https://codesamplex.dev)"

// Cache stores publicness verdicts keyed by canonical purl string.
// Implementations are expected to be backed by localdb.
type Cache interface {
	GetPublicness(ctx context.Context, purl string) (status string, checkedAt time.Time, ok bool)
	SetPublicness(ctx context.Context, purl, status string) error
}

var defaultBaseURLs = map[string]string{
	"npm":      "https://registry.npmjs.org",
	"pypi":     "https://pypi.org",
	"cargo":    "https://crates.io",
	"golang":   "https://proxy.golang.org",
	"gem":      "https://rubygems.org",
	"composer": "https://repo.packagist.org",
	"hex":      "https://hex.pm",
	"pub":      "https://pub.dev",
	"maven":    "https://repo.maven.apache.org/maven2",
}

const defaultHexArchiveBaseURL = "https://repo.hex.pm"

var defaultClient = &http.Client{Timeout: requestTimeout}

// Checker resolves package publicness against the supported registries. Nine
// ecosystems have Public v1 verification adapters; Maven is deliberately A4
// only, so this probe supports exact sample coordinates and Wanted without
// implying that arbitrary Java projects are scanned.
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
	if status, ok := c.CachedPublicness(ctx, p); ok {
		return status
	}
	if !ValidPackageName(p.Ecosystem, p.Name) ||
		(p.Version != "" && !domain.ConcreteResolvedVersion(p.Version)) {
		return scanner.PublicnessUnknown
	}
	key := p.String()
	status := c.probe(ctx, p)
	if status != scanner.PublicnessUnknown && c.Cache != nil {
		c.Cache.SetPublicness(ctx, key, status)
	}
	return status
}

// CachedPublicness answers from the cache alone, and reports whether it could.
//
// It is the whole of Check's cache path, called by Check rather than
// duplicated beside it, so the validation and the TTL cannot drift between the
// two callers. A caller bounding outbound registry traffic asks this first: an
// answer from here reached nobody and must not be charged to that bound.
//
// A PURL parser separates fields; it does not prove that the name has a valid
// shape for its registry. An unescaped '?' or '#' changes the meaning of a URL
// assembled from that name: an exact npm probe for "react?anything" would
// otherwise request /react and grade a made-up package PUBLIC. Validating
// before the cache read as well means an old poisoned verdict cannot bypass
// the corrected boundary.
func (c *Checker) CachedPublicness(ctx context.Context, p domain.PURL) (string, bool) {
	if !ValidPackageName(p.Ecosystem, p.Name) ||
		(p.Version != "" && !domain.ConcreteResolvedVersion(p.Version)) {
		return "", false
	}
	if c.Cache == nil {
		return "", false
	}
	if status, at, ok := c.Cache.GetPublicness(ctx, p.String()); ok && time.Since(at) < cacheTTL {
		return status, true
	}
	return "", false
}

// ValidPackageName reports whether name has a safe public-registry shape.
//
// It is deliberately a conservative common subset of the registries' name
// grammars. Public package names are ASCII and use unreserved path
// characters; only npm scopes, Composer coordinates and Go modules contain
// slashes, and each has a fixed shape. This is both input validation and a
// URL-boundary invariant: query delimiters, fragments, traversal elements
// and extra path segments can never be reinterpreted by net/http or a
// registry proxy.
func ValidPackageName(ecosystem, name string) bool {
	if name == "" || strings.TrimSpace(name) != name {
		return false
	}
	validSegment := func(segment string) bool {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for i := 0; i < len(segment); i++ {
			c := segment[i]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || strings.ContainsRune("-._~", rune(c)) {
				continue
			}
			return false
		}
		return true
	}

	switch ecosystem {
	case "npm":
		if !strings.HasPrefix(name, "@") {
			return !strings.Contains(name, "/") && validSegment(name)
		}
		scope, pkg, ok := strings.Cut(name, "/")
		return ok && !strings.Contains(pkg, "/") && len(scope) > 1 &&
			validSegment(scope[1:]) && validSegment(pkg)
	case "composer", "maven":
		vendor, pkg, ok := strings.Cut(name, "/")
		return ok && !strings.Contains(pkg, "/") && validSegment(vendor) && validSegment(pkg)
	case "golang":
		segments := strings.Split(name, "/")
		if len(segments) < 2 || !strings.Contains(segments[0], ".") {
			return false
		}
		// Go requires the leading domain element to be lower-case LDH plus
		// dots. Later module-path elements may contain upper-case letters,
		// which the proxy represents with !-escaping.
		for i := 0; i < len(segments[0]); i++ {
			c := segments[0][i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
				continue
			}
			return false
		}
		for _, segment := range segments {
			if !validSegment(segment) {
				return false
			}
		}
		return true
	case "pypi", "cargo", "gem", "hex", "pub":
		return !strings.Contains(name, "/") && validSegment(name)
	default:
		return false
	}
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
	// Packagist's v2 metadata endpoint is both the package probe and the
	// authoritative version list. A 200 alone therefore cannot prove the
	// requested release exists; inspect its bounded response body instead.
	if p.Ecosystem == "composer" {
		return c.probeComposer(ctx, p)
	}
	u := c.versionURL(p)
	if u == "" {
		return c.nameStatus(ctx, p)
	}
	method := http.MethodGet
	switch p.Ecosystem {
	case "gem", "hex", "pub":
		// These registries expose immutable release archives. HEAD proves the
		// exact object without downloading package contents.
		method = http.MethodHead
	}
	switch c.requestStatus(ctx, method, u) {
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

// probeComposer checks an exact Composer release in Packagist's p2 metadata.
// The endpoint can be large for long-lived packages, so both network reads
// and JSON decoding are capped. Oversized or malformed metadata is UNKNOWN,
// which downstream treats as private.
func (c *Checker) probeComposer(ctx context.Context, p domain.PURL) string {
	u := c.checkURL(p)
	if u == "" {
		return scanner.PublicnessUnknown
	}
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := newRegistryRequest(reqCtx, http.MethodGet, u)
	if err != nil {
		return scanner.PublicnessUnknown
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return scanner.PublicnessUnknown
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusGone:
		return scanner.PublicnessPrivate
	case http.StatusOK:
	default:
		return scanner.PublicnessUnknown
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxComposerMetadataBody+1))
	if err != nil || len(body) > maxComposerMetadataBody {
		return scanner.PublicnessUnknown
	}
	var metadata struct {
		Packages map[string][]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return scanner.PublicnessUnknown
	}
	for _, release := range metadata.Packages[p.Name] {
		if release.Version == p.Version {
			return scanner.PublicnessPublic
		}
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
	return c.requestStatus(ctx, http.MethodGet, u)
}

// requestStatus returns the HTTP status code, or 0 when the request never
// completed. Bodies are never consumed here; callers use this only for
// endpoints whose status is sufficient evidence.
func (c *Checker) requestStatus(ctx context.Context, method, u string) int {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := newRegistryRequest(reqCtx, method, u)
	if err != nil {
		return 0
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// newRegistryRequest builds an outbound registry probe. Every request in this
// file is built here so the identifying User-Agent cannot be set on one probe
// path and forgotten on another.
func newRegistryRequest(ctx context.Context, method, u string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

func (c *Checker) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultClient
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
// ecosystem outside the supported public-coordinate registries.
func (c *Checker) checkURL(p domain.PURL) string {
	base, ok := c.baseURL(p.Ecosystem)
	if !ok {
		return ""
	}
	switch p.Ecosystem {
	case "npm":
		// PathEscape preserves the npm scope marker while escaping its slash
		// as %2F, the registry's canonical scoped-package endpoint.
		return base + "/" + url.PathEscape(p.Name)
	case "pypi":
		return base + "/pypi/" + url.PathEscape(p.Name) + "/json"
	case "cargo":
		return base + "/api/v1/crates/" + url.PathEscape(p.Name)
	case "golang":
		return base + "/" + escapeGoModulePath(p.Name) + "/@latest"
	case "gem":
		return base + "/api/v1/versions/" + url.PathEscape(p.Name) + ".json"
	case "composer":
		vendor, name, ok := strings.Cut(p.Name, "/")
		if !ok || vendor == "" || name == "" || strings.Contains(name, "/") {
			return ""
		}
		return base + "/p2/" + url.PathEscape(vendor) + "/" + url.PathEscape(name) + ".json"
	case "hex":
		return base + "/api/packages/" + url.PathEscape(p.Name)
	case "pub":
		return base + "/api/packages/" + url.PathEscape(p.Name)
	case "maven":
		group, artifact, ok := strings.Cut(p.Name, "/")
		if !ok || group == "" || artifact == "" || strings.Contains(artifact, "/") {
			return ""
		}
		groupPath := strings.ReplaceAll(group, ".", "/")
		return base + "/" + groupPath + "/" + url.PathEscape(artifact) + "/maven-metadata.xml"
	}
	return ""
}

// escapeGoModulePath applies proxy case encoding and then treats every
// slash-separated element as one URL path segment. ValidPackageName has
// already excluded URL delimiters and traversal elements; preserving '/'
// here is intentional because it is part of a Go module coordinate.
func escapeGoModulePath(path string) string {
	encoded := escapeGoModule(path)
	segments := strings.Split(encoded, "/")
	for i, segment := range segments {
		// The proxy protocol spells an upper-case rune as a literal ! plus
		// its lower-case form. PathEscape may encode !; either wire form is
		// equivalent, but retaining it keeps the documented proxy shape.
		segments[i] = strings.ReplaceAll(url.PathEscape(segment), "%21", "!")
	}
	return strings.Join(segments, "/")
}

func (c *Checker) baseURL(ecosystem string) (string, bool) {
	base, ok := c.BaseURLs[ecosystem]
	if !ok {
		base, ok = defaultBaseURLs[ecosystem]
	}
	return strings.TrimSuffix(base, "/"), ok
}

// versionURL builds the probe for one specific release, or "" when the
// ecosystem is outside the supported public-coordinate registries. Every supported registry
// provides either an exact-release endpoint or authoritative version
// metadata, so a version claim is checkable where it would be published.
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
	case "gem":
		base, _ := c.baseURL(p.Ecosystem)
		return base + "/downloads/" + url.PathEscape(p.Name+"-"+p.Version+".gem")
	case "composer":
		return pkg
	case "hex":
		base, _ := c.baseURL(p.Ecosystem)
		if _, overridden := c.BaseURLs[p.Ecosystem]; !overridden {
			base = defaultHexArchiveBaseURL
		}
		return strings.TrimSuffix(base, "/") + "/tarballs/" + url.PathEscape(p.Name+"-"+p.Version+".tar")
	case "pub":
		base, _ := c.baseURL(p.Ecosystem)
		return base + "/api/archives/" + url.PathEscape(p.Name+"-"+p.Version+".tar.gz")
	case "maven":
		base, _ := c.baseURL(p.Ecosystem)
		group, artifact, ok := strings.Cut(p.Name, "/")
		if !ok || group == "" || artifact == "" || strings.Contains(artifact, "/") {
			return ""
		}
		groupPath := strings.ReplaceAll(group, ".", "/")
		version := url.PathEscape(p.Version)
		return base + "/" + groupPath + "/" + url.PathEscape(artifact) + "/" + version + "/" +
			url.PathEscape(artifact+"-"+p.Version+".pom")
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
