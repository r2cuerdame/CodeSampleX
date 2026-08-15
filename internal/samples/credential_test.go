package samples

import (
	"os"
	"path/filepath"
	"testing"
)

// The credential check required a colon — user:token@host — so it caught
// the rare shape and missed every single-token one, which is what a token
// actually looks like: a GitHub PAT, an npm registry token, or base64 of
// "user:password", where the colon is hidden inside the encoding.
//
// A lockfile is otherwise exempt from the URL allowlist, because its hosts
// are maintainer-chosen and no allowlist can cover them. This check is the
// only thing standing between a lockfile and a leaked token.
func TestCredentialsInAURLAreCaughtWithoutAColon(t *testing.T) {
	for _, u := range []string{
		"https://ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA@github.com/x/y.git",
		"https://npm_TTTTTTTTTTTTTTTTTTTT@registry.npmjs.org/",
		"https://dXNlcjpwYXNzd29yZA==@registry.example.com/pkg",
		"https://user:hunter2@registry.example.com/pkg",
		"http://token@internal-registry/simple/",
	} {
		if !credentialURL(u) {
			t.Errorf("not flagged: %s", u)
		}
	}

	// Conventions that are not secrets, and ordinary URLs.
	for _, u := range []string{
		"git+ssh://git@github.com/x/y.git",
		"https://registry.npmjs.org/@scope/pkg/-/pkg-1.0.0.tgz",
		"https://pypi.org/simple/requests/",
		"https://example.com/a/b?c=d",
	} {
		if credentialURL(u) {
			t.Errorf("false positive: %s", u)
		}
	}
}

// End to end: a token in a lockfile URL blocks publish.
func TestATokenInALockfileIsAFinding(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.js", "export default 1\n")
	write("package-lock.json",
		`{"packages":{"node_modules/x":{"resolved":"https://ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA@registry.npmjs.org/x/-/x-1.0.0.tgz"}}}`)

	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var caught bool
	for _, f := range findings {
		if f.Kind == KindURL {
			caught = true
		}
	}
	if !caught {
		t.Errorf("a token in a lockfile URL was not flagged: %+v", findings)
	}
}
