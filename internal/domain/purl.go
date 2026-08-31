// Package domain holds CodeSampleX's core types and pure logic.
// It has no I/O and no dependencies outside the standard library.
package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PURL identifies a public package as pkg:<ecosystem>/<name>@<version>.
// Ecosystems in Public v1: npm, pypi, cargo, golang, maven.
// golang names may contain '/' (module paths); npm names may be scoped (@scope/name).
type PURL struct {
	Ecosystem string
	Name      string
	Version   string
}

// AllowedEcosystems is the Public v1 package/receipt allowlist. Scanner
// coverage is a separate claim: verification-only ecosystems such as Maven
// may publish signed sample evidence without scanning arbitrary local projects.
//
// gem, hex and pub have exactly that standing and were missing from it. The
// client scanner ships adapters for npm, pypi, golang and cargo only, so
// maven, gem, hex and pub are all here on the verification-only footing the
// paragraph above describes -- but the three added last refused every run
// this network performed in them. Measured after the receipt backfill: 8,467
// runs recorded, 1,467 refused, and every refusal was one of these three. The
// 938 snapshot rows still reading "never measured" were those three
// ecosystems and nothing else.
var AllowedEcosystems = map[string]bool{
	"npm":    true,
	"pypi":   true,
	"cargo":  true,
	"golang": true,
	"maven":  true,
	"gem":    true,
	"hex":    true,
	"pub":    true,
}

// VerifiableEcosystems is AllowedEcosystems as a sorted list, for the one
// place that has to tell a caller what it may use instead.
//
// A refusal that does not say what would have worked leaves an agent guessing,
// and guessing here means writing a whole sample for another ecosystem this
// network also cannot verify.
func VerifiableEcosystems() []string {
	out := make([]string, 0, len(AllowedEcosystems))
	for eco := range AllowedEcosystems {
		out = append(out, eco)
	}
	sort.Strings(out)
	return out
}

// ParsePURL parses a package URL string. It accepts both canonical
// percent-encoded scoped npm names (pkg:npm/%40scope/name@1.0.0) and the
// lenient raw form (pkg:npm/@scope/name@1.0.0).
func ParsePURL(s string) (PURL, error) {
	rest, ok := strings.CutPrefix(s, "pkg:")
	if !ok {
		return PURL{}, fmt.Errorf("purl: missing pkg: prefix in %q", s)
	}
	eco, rest, ok := strings.Cut(rest, "/")
	if !ok || eco == "" || rest == "" {
		return PURL{}, fmt.Errorf("purl: missing ecosystem or name in %q", s)
	}
	eco = strings.ToLower(eco)
	at := strings.LastIndex(rest, "@")
	if at <= 0 {
		return PURL{}, fmt.Errorf("purl: missing version in %q", s)
	}
	name, version := rest[:at], rest[at+1:]
	if name == "" || version == "" || strings.HasSuffix(name, "/") {
		return PURL{}, fmt.Errorf("purl: empty name or version in %q", s)
	}
	decoded, err := url.PathUnescape(name)
	if err != nil {
		return PURL{}, fmt.Errorf("purl: bad escaping in %q: %w", s, err)
	}
	return PURL{Ecosystem: eco, Name: decoded, Version: CanonicalVersion(eco, version)}, nil
}

// CanonicalVersion repairs the one spelling difference that is a malformation
// rather than a choice: a Go module version without its leading "v".
//
// Go module versions are canonically v-prefixed, so a bare `5.10.0` for golang
// names no release — but it parses, stores and renders exactly like one. The
// corpus therefore held the same release twice:
// `github.com/jackc/pgx/v5@v5.10.0` at rank 4 on the wanted board with
// `@5.10.0` as a separate row below it, their asks not adding up, one question
// shown to a reader as two entries. Production on 2026-08-31: 8 package rows
// across 6 names, and 16 wanted rows.
//
// It is deliberately narrow in both directions. Every other ecosystem writes
// bare versions canonically, so prefixing there would invent the split this
// removes. And a version that does not start with a digit is not a Go release
// missing its prefix — "latest" is a channel, and "vlatest" would be a
// coordinate nobody published.
//
// PURL.Major() has compensated for the missing prefix at shard-key time for as
// long as both shapes existed. That stays: it covers PURLs built directly
// rather than parsed, and defends the shard keys if a new construction path
// ever skips this.
func CanonicalVersion(ecosystem, version string) string {
	if ecosystem != "golang" || version == "" || strings.HasPrefix(version, "v") {
		return version
	}
	if version[0] < '0' || version[0] > '9' {
		return version
	}
	return "v" + version
}

// String renders the canonical purl form; a leading '@' in npm scoped names
// is percent-encoded per the PURL spec.
func (p PURL) String() string {
	name := p.Name
	if strings.HasPrefix(name, "@") {
		name = "%40" + name[1:]
	}
	return "pkg:" + p.Ecosystem + "/" + name + "@" + p.Version
}

// ConcreteResolvedVersion reports whether v has the shape of a registry
// release selected by a resolver, rather than a request/range/protocol. It
// is intentionally cross-ecosystem and conservative: public versions used
// by the supported resolvers fit this ASCII set, while ^1, >=2, URLs,
// workspace references and whitespace do not. At least one digit is
// required so channel names such as "latest" cannot become signed version
// evidence.
func ConcreteResolvedVersion(v string) bool {
	if v == "" || strings.TrimSpace(v) != v {
		return false
	}
	hasDigit := false
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r == '.', r == '_', r == '-', r == '+', r == '!':
		default:
			return false
		}
	}
	return hasDigit
}

// AnyVersionPattern is this package under any version, in the CANONICAL
// stored form, for a SQL LIKE prefilter.
//
// String() escapes a leading "@" to "%40", so a scoped npm package is
// stored as pkg:npm/%40types/node@20.0.0 — and callers that built the
// pattern from the raw name asked for pkg:npm/@types/node@%, which matches
// nothing. Every scoped package (@types/*, @babel/*, @tanstack/*, a large
// share of npm) silently failed to find its own samples.
//
// The escaped "%40" leaves a literal % in the pattern, which LIKE reads as
// a wildcard. That makes the prefilter slightly looser, never tighter, and
// the exact ecosystem/name comparison downstream is what decides.
func (p PURL) AnyVersionPattern() string {
	return PURL{Ecosystem: p.Ecosystem, Name: p.Name, Version: "%"}.String()
}

// versionSegments splits the version into numeric-ish dot segments,
// dropping any pre-release/build suffix.
func (p PURL) versionSegments() []string {
	v := p.Version
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	return strings.Split(v, ".")
}

// Major returns the major version bucket used for shard keys.
// golang keeps its "v" prefix ("v1"); other ecosystems return the bare
// first segment ("1").
func (p PURL) Major() string {
	seg := p.versionSegments()[0]
	if p.Ecosystem == "golang" && !strings.HasPrefix(seg, "v") {
		return "v" + seg
	}
	return seg
}

// BreakingBucket returns the version line within which semver promises no
// breaking change: "1" for 1.2.3, but "0.6" for 0.6.20.
//
// Major() is deliberately NOT this, because it also generates shard keys and
// changing it would invalidate every shard. But grading with it treated 0.x
// as a stable major, so axum 0.6 against 0.8 was reported as a minor
// difference when semver makes a 0.x minor bump exactly as breaking as a
// major one. Pre-1.0 is where most Rust and a lot of Dart lives.
func (p PURL) BreakingBucket() string {
	segs := p.versionSegments()
	if len(segs) == 0 {
		return ""
	}
	if segs[0] != "0" && segs[0] != "v0" {
		return p.Major()
	}
	if len(segs) < 2 {
		return p.Major()
	}
	return p.Major() + "." + segs[1]
}

// MajorMinor returns "major.minor" ("1.12"; golang "v1.2"). A version with a
// single segment returns just the major bucket.
func (p PURL) MajorMinor() string {
	segs := p.versionSegments()
	if len(segs) < 2 {
		return p.Major()
	}
	return p.Major() + "." + segs[1]
}

// CompareVersions orders two version strings the way a reader expects,
// which string comparison does not: "7.0.3" sorts above "14.0.1" because
// '7' > '1'. The record page picked the newest version that way and told
// every reader that npm/uuid was at 7.0.3 when the only evidence the
// network held was 14.0.1 — a wrong fact on the most-read page, about the
// one thing this site exists to be right about.
//
// Numeric segments compare numerically, non-numeric ones lexically, and a
// version with fewer segments sorts below one that extends it ("1.2" <
// "1.2.1"). A pre-release suffix is ranked below the release it precedes,
// per semver: 1.2.0-rc1 < 1.2.0.
//
// It returns -1, 0 or 1. This is deliberately not a full semver parser:
// the input is whatever a lockfile resolved, across nine ecosystems, and a
// comparator that rejects what it cannot parse would be worse than one
// that degrades to a sensible order.
func CompareVersions(a, b string) int {
	ac, bc := coreVersion(a), coreVersion(b)
	if c := compareSegments(ac, bc); c != 0 {
		return c
	}
	// Equal cores: a pre-release loses to the plain release.
	ap, bp := preRelease(a), preRelease(b)
	switch {
	case ap == "" && bp == "":
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	return compareSegments(ap, bp)
}

// coreVersion strips a leading "v" and any pre-release or build suffix.
func coreVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	return v
}

// preRelease returns the suffix after '-', without build metadata.
func preRelease(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Build metadata comes off FIRST. Semver ignores it for precedence and
	// a version carrying it is not a prerelease -- but a hyphen inside it
	// looked like the prerelease separator, so 1.2.0+build-1 was read as
	// prerelease "1" and sorted BELOW plain 1.2.0. Version order decides
	// which release is "V-1", so a mis-ordered pair points the regression
	// rule at the wrong comparison.
	if j := strings.IndexByte(v, '+'); j >= 0 {
		v = v[:j]
	}
	i := strings.IndexByte(v, '-')
	if i < 0 {
		return ""
	}
	return v[i+1:]
}

func compareSegments(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if c := compareSegment(x, y); c != 0 {
			return c
		}
	}
	return 0
}

func compareSegment(x, y string) int {
	if x == y {
		return 0
	}
	// A missing segment sorts below a present one ("1.2" < "1.2.1").
	if x == "" {
		return -1
	}
	if y == "" {
		return 1
	}
	xi, xerr := strconv.Atoi(x)
	yi, yerr := strconv.Atoi(y)
	switch {
	case xerr == nil && yerr == nil:
		if xi != yi {
			if xi < yi {
				return -1
			}
			return 1
		}
		return 0
	case xerr == nil:
		return 1 // numeric outranks alphanumeric ("1.10" > "1.beta")
	case yerr == nil:
		return -1
	}
	return compareMixed(x, y)
}

// compareMixed orders two alphanumeric identifiers by walking their letter
// and digit runs in step, so rc2 < rc10 rather than the other way round.
//
// Strict semver would compare "rc2" and "rc10" as whole ASCII strings and
// call rc10 the earlier one, because a pre-release identifier only splits
// on dots. Nobody who writes rc10 means that, and this comparator exists to
// order a list a person reads.
func compareMixed(x, y string) int {
	for x != "" && y != "" {
		xr, xrest := leadingRun(x)
		yr, yrest := leadingRun(y)
		xd := xr[0] >= '0' && xr[0] <= '9'
		yd := yr[0] >= '0' && yr[0] <= '9'
		switch {
		case xd && yd:
			xi, _ := strconv.Atoi(xr)
			yi, _ := strconv.Atoi(yr)
			if xi != yi {
				if xi < yi {
					return -1
				}
				return 1
			}
		case xr != yr:
			if xr < yr {
				return -1
			}
			return 1
		}
		x, y = xrest, yrest
	}
	switch {
	case x == "" && y == "":
		return 0
	case x == "":
		return -1
	}
	return 1
}

// leadingRun splits off the leading run of digits or of non-digits.
func leadingRun(s string) (run, rest string) {
	digit := s[0] >= '0' && s[0] <= '9'
	for i := 0; i < len(s); i++ {
		if (s[i] >= '0' && s[i] <= '9') != digit {
			return s[:i], s[i:]
		}
	}
	return s, ""
}

// PinnedImageReference is the admission shape for a verifier image: any
// reference at all, bound to an immutable digest.
//
// It lives here because two places have to agree on it and did not. The
// server admits a receipt with this shape; the offline receipt audit had its
// own stricter copy that additionally demanded a tag, so a receipt the server
// had ACCEPTED — repo@sha256:… with no tag — was counted by the audit as not
// digest-pinned. The audit's whole job is to re-derive the server's answer,
// and it cannot do that from a different rule.
var PinnedImageReference = regexp.MustCompile(`^[^@\s]+@(sha256:[0-9a-f]{64})$`)
