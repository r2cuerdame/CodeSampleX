package python

import "testing"

// The source patterns only caught editable, path, git and directory
// entries. A package pulled from a company Artifactory looks like an
// ordinary registry entry, so it was reported UNKNOWN, the registry
// checker found the NAME on public PyPI and upgraded it to PUBLIC, and an
// observation went out for pkg:pypi/requests@2.31.0-corp1 — an internal
// build string leaving the machine, plus false public evidence for a
// version PyPI never published.
func TestAPackageFromAPrivateIndexIsPrivate(t *testing.T) {
	uv := []byte(`
[[package]]
name = "requests"
version = "2.31.0-corp1"
source = { registry = "https://artifactory.acmecorp.io/api/pypi/pypi/simple" }

[[package]]
name = "certifi"
version = "2024.7.4"
source = { registry = "https://pypi.org/simple" }
`)
	got := parseLockPackages(uv, uvPrivateSourceRe)
	if len(got) != 2 {
		t.Fatalf("parsed %d entries", len(got))
	}
	byName := map[string]lockEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if !byName["requests"].Private {
		t.Error("a package from a company index was not marked private")
	}
	if byName["certifi"].Private {
		t.Error("a package from public PyPI was marked private")
	}
}

// poetry.lock spells it differently, and the same rule has to hold.
func TestPoetryLegacySourceIsPrivate(t *testing.T) {
	poetry := []byte(`
[[package]]
name = "internal-sdk"
version = "1.4.0"

[package.source]
type = "legacy"
url = "https://pypi.internal.acmecorp.io/simple"
reference = "corp"

[[package]]
name = "urllib3"
version = "2.2.1"
`)
	got := parseLockPackages(poetry, poetryPrivateSourceRe)
	byName := map[string]lockEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if !byName["internal-sdk"].Private {
		t.Error("a package from an internal index was not marked private")
	}
	if byName["urllib3"].Private {
		t.Error("a package with no source table was marked private")
	}
}
