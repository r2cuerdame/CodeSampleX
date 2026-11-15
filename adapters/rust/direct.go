package rust

import "strings"

// lockKey names one [[package]] entry of Cargo.lock.
type lockKey struct {
	name, version, source string
}

func keyOf(e lockEntry) lockKey { return lockKey{e.name, e.version, e.source} }

// rootSelections returns the lock entries the root package's own dependency
// list in Cargo.lock points at.
//
// The manifest names a dependency, not a version, so when Cargo.lock holds
// two copies of one crate — rand 0.8.5 the project asked for and rand 0.7.3
// some other crate pulled in — only the root entry's list says which copy is
// the project's. Cargo writes an entry there as "name" (only when the name is
// unique in the graph), "name version", or "name version (source)". An entry
// that does not resolve to exactly one package selects nothing: a copy we
// cannot tell apart from another is not reported as chosen.
//
// No root entry (a virtual workspace, a lockfile without one) yields an empty
// set.
func rootSelections(pkgs []lockPackage, rootName string) map[lockKey]bool {
	byName := map[string][]lockEntry{}
	for _, p := range pkgs {
		byName[p.entry.name] = append(byName[p.entry.name], p.entry)
	}
	chosen := map[lockKey]bool{}
	if rootName == "" {
		return chosen
	}
	for _, p := range pkgs {
		if p.entry.source != "" || p.entry.name != rootName {
			continue
		}
		for _, dep := range p.deps {
			name, version, source, ok := parseLockDepRef(dep)
			if !ok {
				continue
			}
			var match lockEntry
			matches := 0
			for _, c := range byName[name] {
				if version != "" && c.version != version {
					continue
				}
				if source != "" && sourceBase(c.source) != sourceBase(source) {
					continue
				}
				match = c
				matches++
			}
			if matches == 1 {
				chosen[keyOf(match)] = true
			}
		}
	}
	return chosen
}

// parseLockDepRef reads one entry of a Cargo.lock dependencies list.
func parseLockDepRef(dep string) (name, version, source string, ok bool) {
	dep = strings.TrimSpace(dep)
	if i := strings.Index(dep, " ("); i >= 0 {
		if !strings.HasSuffix(dep, ")") {
			return "", "", "", false
		}
		source = strings.TrimSpace(dep[i+2 : len(dep)-1])
		dep = dep[:i]
	}
	fields := strings.Fields(dep)
	switch len(fields) {
	case 1:
		return fields[0], "", source, true
	case 2:
		return fields[0], fields[1], source, true
	default:
		return "", "", "", false
	}
}

// sourceBase drops a git source's #revision, which a package's source line
// carries and a reference to it may not.
func sourceBase(source string) string {
	if i := strings.IndexByte(source, '#'); i >= 0 {
		return source[:i]
	}
	return source
}
