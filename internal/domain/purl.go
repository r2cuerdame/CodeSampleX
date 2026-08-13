// Package domain holds CodeSampleX's core types and pure logic.
// It has no I/O and no dependencies outside the standard library.
package domain

import (
	"fmt"
	"net/url"
	"strings"
)

// PURL identifies a public package as pkg:<ecosystem>/<name>@<version>.
// Ecosystems in Public v1: npm, pypi, cargo, golang.
// golang names may contain '/' (module paths); npm names may be scoped (@scope/name).
type PURL struct {
	Ecosystem string
	Name      string
	Version   string
}

// AllowedEcosystems is the Public v1 automatic-collection allowlist (goal.md §8.1).
var AllowedEcosystems = map[string]bool{
	"npm":    true,
	"pypi":   true,
	"cargo":  true,
	"golang": true,
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
	return PURL{Ecosystem: eco, Name: decoded, Version: version}, nil
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

// MajorMinor returns "major.minor" ("1.12"; golang "v1.2"). A version with a
// single segment returns just the major bucket.
func (p PURL) MajorMinor() string {
	segs := p.versionSegments()
	if len(segs) < 2 {
		return p.Major()
	}
	return p.Major() + "." + segs[1]
}
