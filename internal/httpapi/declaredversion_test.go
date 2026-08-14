package httpapi

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Two published samples claimed a version their contract never compiled.
// A Cargo.toml written as version = "0.4.42" is a caret requirement, cargo
// resolved 0.4.45, the contract passed against 0.4.45 — and the manifest
// went on saying 0.4.42, so the sample was evidence about a version it had
// never been run on. Nothing anywhere would have caught it.
func TestManifestCannotClaimAVersionTheLockfileDidNotResolve(t *testing.T) {
	m := domain.SampleManifest{Packages: []string{"pkg:cargo/chrono@0.4.42"}}
	lock := map[string]string{"cargo.lock": `[[package]]
name = "chrono"
version = "0.4.45"
`}
	err := checkDeclaredVersions(m, lock)
	if err == nil {
		t.Fatal("a manifest claiming an unresolved version was accepted")
	}
	if !strings.Contains(err.Error(), "0.4.45") {
		t.Errorf("the error does not name what the lockfile actually resolved: %v", err)
	}

	m.Packages = []string{"pkg:cargo/chrono@0.4.45"}
	if err := checkDeclaredVersions(m, lock); err != nil {
		t.Errorf("the truthful manifest was refused: %v", err)
	}
}

func TestDeclaredVersionCheckReadsNpmAndGemLocks(t *testing.T) {
	npm := map[string]string{"package-lock.json": `{"packages":{"node_modules/axios":{"version":"1.19.0"}}}`}
	if err := checkDeclaredVersions(domain.SampleManifest{
		Packages: []string{"pkg:npm/axios@1.12.0"},
	}, npm); err == nil {
		t.Error("npm mismatch not caught")
	}

	gem := map[string]string{"gemfile.lock": `GEM
  specs:
    faraday (2.11.0)
`}
	if err := checkDeclaredVersions(domain.SampleManifest{
		Packages: []string{"pkg:gem/faraday@2.14.3"},
	}, gem); err == nil {
		t.Error("gem mismatch not caught")
	}
}

// Certainty is the whole point: a format it cannot read, or a package the
// lockfile does not mention, must pass rather than block an honest sample.
func TestDeclaredVersionCheckStaysSilentWhenUnsure(t *testing.T) {
	m := domain.SampleManifest{Packages: []string{"pkg:cargo/chrono@0.4.42"}}
	other := map[string]string{"cargo.lock": `[[package]]
name = "serde"
version = "1.0.0"
`}
	for _, lock := range []map[string]string{
		{},
		{"cargo.lock": "not a lockfile at all"},
		other,
	} {
		if err := checkDeclaredVersions(m, lock); err != nil {
			t.Errorf("refused when it could not be certain: %v", err)
		}
	}
}
