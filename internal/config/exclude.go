package config

import "strings"

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
	forms := []string{
		strings.ToLower(purl),
		strings.ToLower(stripVersion(purl)),
		strings.ToLower(ecosystem + "/" + name),
		strings.ToLower(name),
	}
	for _, raw := range c.ExcludedPackages {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" {
			continue
		}
		for _, f := range forms {
			if f != "" && f == e {
				return true
			}
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
