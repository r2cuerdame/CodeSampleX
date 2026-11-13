package node

import "strings"

// isURLSpec reports whether a manifest specifier asks for a tarball by URL
// ("https://example.com/foo-1.2.3.tgz") instead of a registry range.
//
// What arrives from such a URL is whatever that server sent, which need not
// be the registry's release of the same name and version: a patched fork is
// the usual reason to write one. The lockfile still records the tarball's own
// name and version, so the copy looked exactly like the registry one, the
// publicness check found that release on npm, and evidence about a build that
// never used it was uploaded under its name.
//
// Only SPECIFIERS are judged this way, never a lockfile's resolved field: an
// ordinary registry install resolves to an https URL too
// (https://registry.npmjs.org/..., or a company mirror), and that is what
// keeps isPrivateSpec, which reads both, from treating https as private.
func isURLSpec(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "http:") || strings.HasPrefix(s, "https:")
}

// urlInstalledKeys returns the package-lock.json keys of every installed copy
// that some manifest in the lockfile asked for by URL.
//
// A URL specifier is recorded only on the package that wrote it, while the
// copy it installed sits under node_modules wherever npm placed it, so each
// one is followed with npm's own lookup (resolveChildPath) to the copy that
// requiring package actually loads. That covers a dependency of a dependency
// and a workspace member's own request alike, whichever directory is scanned.
//
// Installed packages contribute only what npm installs for them; the root and
// workspace members (keys outside node_modules) contribute every section.
func urlInstalledKeys(packages map[string]npmLockEntry) map[string]bool {
	keys := map[string]bool{}
	for path, e := range packages {
		if e.Link {
			continue // a link's own entry is its target's, read under that key
		}
		sections := []map[string]string{e.Dependencies, e.OptionalDependencies, e.PeerDependencies}
		if !strings.Contains(path, "node_modules/") {
			sections = append(sections, e.DevDependencies)
		}
		for _, specs := range sections {
			for child, spec := range specs {
				if child == "" || !isURLSpec(spec) {
					continue
				}
				if key, ok := resolveChildPath(packages, path, child); ok {
					keys[key] = true
				}
			}
		}
	}
	return keys
}
