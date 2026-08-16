package goadapter

import "testing"

// resolveOne parses a go.mod and returns what the adapter would record for
// one module path.
func resolveOne(t *testing.T, gomod, path string) (name, version string) {
	t.Helper()
	requires, localReplaced, moduleReplaced := parseGoMod(gomod)
	for _, r := range requires {
		if r.path != path {
			continue
		}
		name, version = r.path, r.version
		if rep, ok := pickReplacement(moduleReplaced[r.path], r.version); ok {
			name, version = rep.path, rep.version
		}
		if governs(localReplaced[r.path], r.version) {
			return name, "PRIVATE"
		}
		return name, version
	}
	t.Fatalf("%s not required by this go.mod", path)
	return "", ""
}

// A go.mod may legally carry several replace directives for one path as
// long as the left-hand versions differ. Keeping only the last one parsed
// made FILE ORDER decide which version got recorded — and when the
// survivor was the non-matching one, no replacement applied at all, so
// evidence from a v0.31.0 build was filed under v0.21.0.
func TestSeveralReplacesForOnePathPicksTheOneThatApplies(t *testing.T) {
	const live = "replace golang.org/x/crypto v0.21.0 => golang.org/x/crypto v0.31.0"
	const stale = "replace golang.org/x/crypto v0.0.0-20220314234659-1baeb1ce4c0b => golang.org/x/crypto v0.42.0"
	head := "module example.com/app\ngo 1.24\nrequire golang.org/x/crypto v0.21.0\n"

	for _, order := range []struct{ name, body string }{
		{"live first", head + live + "\n" + stale + "\n"},
		{"stale first", head + stale + "\n" + live + "\n"},
	} {
		_, version := resolveOne(t, order.body, "golang.org/x/crypto")
		if version != "v0.31.0" {
			t.Errorf("%s: recorded %s, want v0.31.0 — the version that compiles "+
				"must not depend on which line came last", order.name, version)
		}
	}
}

// go applies an exact `path vX => …` over a version-less catch-all for the
// same path, whichever order they are written in. This is the ordinary
// shape of a CVE bump pinned for one version above a blanket redirect.
func TestExactReplaceOutranksTheWildcard(t *testing.T) {
	head := "module example.com/app\ngo 1.24\nrequire golang.org/x/net v0.17.0\n"
	const exact = "replace golang.org/x/net v0.17.0 => golang.org/x/net v0.38.0"
	const wild = "replace golang.org/x/net => golang.org/x/net v0.20.0"

	for _, body := range []string{head + exact + "\n" + wild + "\n", head + wild + "\n" + exact + "\n"} {
		if _, version := resolveOne(t, body, "golang.org/x/net"); version != "v0.38.0" {
			t.Errorf("recorded %s, want v0.38.0: the exact directive is the one go applies", version)
		}
	}
}

// A wildcard alone still applies to everything.
func TestWildcardStillAppliesWhenNoExactDirectiveMatches(t *testing.T) {
	body := "module example.com/app\ngo 1.24\nrequire golang.org/x/net v0.17.0\n" +
		"replace golang.org/x/net => golang.org/x/net v0.20.0\n"
	if _, version := resolveOne(t, body, "golang.org/x/net"); version != "v0.20.0" {
		t.Errorf("recorded %s, want v0.20.0", version)
	}
}

// A local replace marks a module private only when it governs the version
// actually selected. Marking on the path alone suppressed evidence that
// was publishable, which fails closed but still costs a sample.
func TestLocalReplaceOnlyMarksPrivateWhenItGoverns(t *testing.T) {
	head := "module example.com/app\ngo 1.24\nrequire example.com/x v0.2.0\n"
	notGoverning := head +
		"replace example.com/x v0.1.0 => ../localcopy\n" +
		"replace example.com/x v0.2.0 => example.com/y v0.3.0\n"
	if name, version := resolveOne(t, notGoverning, "example.com/x"); version == "PRIVATE" {
		t.Errorf("marked private by a directive for a different version (got %s %s)", name, version)
	}
	governing := head + "replace example.com/x v0.2.0 => ../localcopy\n"
	if _, version := resolveOne(t, governing, "example.com/x"); version != "PRIVATE" {
		t.Error("a local replace for the selected version must still mark the module private")
	}
}
