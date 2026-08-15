package config

import (
	"net/url"
	"strings"
)

// IsExcluded reports whether the user asked for this package to be left
// out of everything that leaves the machine.
//
// The setting was accepted, saved, echoed back by `csx config get` and
// consulted by nothing at all: excluded packages were still recorded as
// observations, still uploaded, and still named in the shard warm list —
// which asks the server about them by name, so the exclusion leaked the
// very interest it was meant to hide.
//
// Matching is deliberately generous, because a person typing this setting
// is stating an intent, not writing a query. Any of these excludes
// pkg:npm/@acme/widgets@1.2.3:
//
//	pkg:npm/@acme/widgets@1.2.3   the exact purl
//	pkg:npm/@acme/widgets         the purl without a version
//	npm/@acme/widgets             ecosystem and name
//	@acme/widgets                 the name alone, every ecosystem
//
// A privacy setting that silently matches less than the user meant is
// worse than one that matches a little more, so ambiguity resolves toward
// excluding.
func (c *Config) IsExcluded(purl, ecosystem, name string) bool {
	if c == nil || len(c.ExcludedPackages) == 0 {
		return false
	}
	forms := map[string]bool{}
	add := func(v string) {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			return
		}
		forms[v] = true
		// A purl percent-encodes the parts of a name that are not
		// path-safe, so PURL.String() renders @acme/widgets as
		// pkg:npm/%40acme/widgets. Nobody types that, and comparing only
		// the encoded form meant every scoped npm package — the most
		// common shape there is — silently failed to match its own
		// exclusion entry.
		if dec, err := url.PathUnescape(v); err == nil {
			forms[dec] = true
		}
	}
	add(purl)
	add(stripVersion(purl))
	add(ecosystem + "/" + name)
	add(name)

	for _, raw := range c.ExcludedPackages {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" {
			continue
		}
		if forms[e] {
			return true
		}
		if dec, err := url.PathUnescape(e); err == nil && forms[dec] {
			return true
		}
	}
	return false
}

// stripVersion drops the "@version" suffix from a purl, leaving any "@" in
// a scoped npm name alone.
func stripVersion(purl string) string {
	if i := strings.LastIndexByte(purl, '@'); i > strings.IndexByte(purl, '/') {
		return purl[:i]
	}
	return purl
}
