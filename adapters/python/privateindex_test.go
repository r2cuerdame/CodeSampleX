package python

import (
	"regexp"
	"testing"
)

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

func lockByName(t *testing.T, got []lockEntry) map[string]lockEntry {
	t.Helper()
	byName := map[string]lockEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	return byName
}

// poetry writes [package.dependencies] before [package.source]. The first url
// in the block used to decide, so a dependency pinned to a public file host
// made a package from a company index look public.
func TestPoetryDependencyURLDoesNotHideAPrivateSource(t *testing.T) {
	poetry := []byte(`
[[package]]
name = "internal-sdk"
version = "1.4.0"

[package.dependencies]
foo = {url = "https://files.pythonhosted.org/packages/foo-1.0.0-py3-none-any.whl"}

[package.source]
type = "legacy"
url = "https://pypi.internal.acmecorp.io/simple"
reference = "corp"
`)
	got := lockByName(t, parseLockPackages(poetry, poetryPrivateSourceRe))
	if e, ok := got["internal-sdk"]; !ok || !e.Private {
		t.Errorf("a package from an internal index was not marked private: %+v", got)
	}
}

// The reverse: a dependency on a vcs url must not hide the parent as private.
func TestPoetryDependencyURLDoesNotHideAPublicPackage(t *testing.T) {
	poetry := []byte(`
[[package]]
name = "fastapi"
version = "0.116.0"

[package.dependencies]
starlette = {git = "https://github.com/encode/starlette.git", rev = "main"}
helper = {url = "https://github.com/acme/helper/archive/main.zip"}

[[package]]
name = "pydantic"
version = "2.11.0"

[package.dependencies]
typing-extensions = ">=4.6"

[package.source]
type = "legacy"
url = "https://pypi.org/simple"
reference = "pypi-mirror"
`)
	got := lockByName(t, parseLockPackages(poetry, poetryPrivateSourceRe))
	for _, name := range []string{"fastapi", "pydantic"} {
		if e, ok := got[name]; !ok || e.Private {
			t.Errorf("%s from public PyPI was marked private or dropped: %+v", name, got)
		}
	}
}

// uv puts sdist and wheel urls in the same block as the source; only the
// source table decides.
func TestUVArtifactURLsDoNotDecideTheSource(t *testing.T) {
	uv := []byte(`
version = 1

[[package]]
name = "certifi"
version = "2024.7.4"
source = { registry = "https://pypi.org/simple" }
sdist = { url = "https://mirror.acmecorp.io/certifi-2024.7.4.tar.gz", hash = "sha256:00" }
wheels = [
    { url = "https://github.com/acme/certifi/releases/certifi-2024.7.4-py3-none-any.whl", hash = "sha256:00" },
]

[[package]]
name = "requests"
version = "2.31.0-corp1"
source = { registry = "https://artifactory.acmecorp.io/api/pypi/pypi/simple" }
sdist = { url = "https://files.pythonhosted.org/packages/requests-2.31.0.tar.gz", hash = "sha256:00" }
dependencies = [
    { name = "certifi" },
]
`)
	got := lockByName(t, parseLockPackages(uv, uvPrivateSourceRe))
	if e, ok := got["certifi"]; !ok || e.Private {
		t.Errorf("a PyPI package was marked private by its artifact urls: %+v", got)
	}
	if e, ok := got["requests"]; !ok || !e.Private {
		t.Errorf("a company-index package was made public by its sdist url: %+v", got)
	}
}

// Sources that were private before stay private: local paths, vcs, editable
// installs, direct urls, and a registry that is not an http(s) index.
func TestLocalAndVCSSourcesStayPrivate(t *testing.T) {
	uv := []byte(`
version = 1

[[package]]
name = "editable-pkg"
version = "0.1.0"
source = { editable = "." }

[[package]]
name = "virtual-pkg"
version = "0.1.0"
source = { virtual = "." }

[[package]]
name = "dir-pkg"
version = "0.1.0"
source = { directory = "../dir-pkg" }

[[package]]
name = "path-pkg"
version = "0.1.0"
source = { path = "../wheels/path_pkg-0.1.0-py3-none-any.whl" }

[[package]]
name = "git-pkg"
version = "0.1.0"
source = { git = "https://github.com/acme/git-pkg?rev=main#0000000" }

[[package]]
name = "url-pkg"
version = "0.1.0"
source = { url = "https://files.pythonhosted.org/packages/url_pkg-0.1.0.tar.gz" }

[[package]]
name = "local-registry-pkg"
version = "0.1.0"
source = { registry = "../local-index" }
`)
	uvGot := lockByName(t, parseLockPackages(uv, uvPrivateSourceRe))
	if len(uvGot) != 7 {
		t.Fatalf("parsed %d uv entries: %+v", len(uvGot), uvGot)
	}
	for name, e := range uvGot {
		if !e.Private {
			t.Errorf("uv %s was not marked private", name)
		}
	}

	poetry := []byte(`
[[package]]
name = "dir-pkg"
version = "0.1.0"

[package.source]
type = "directory"
url = "../dir-pkg"

[[package]]
name = "file-pkg"
version = "0.1.0"

[package.source]
type = "file"
url = "../wheels/file_pkg-0.1.0-py3-none-any.whl"

[[package]]
name = "git-pkg"
version = "0.1.0"

[package.source]
type = "git"
url = "https://github.com/acme/git-pkg.git"
reference = "main"
resolved_reference = "0000000"

[[package]]
name = "url-pkg"
version = "0.1.0"

[package.source]
type = "url"
url = "https://files.pythonhosted.org/packages/url_pkg-0.1.0.tar.gz"
`)
	got := lockByName(t, parseLockPackages(poetry, poetryPrivateSourceRe))
	if len(got) != 4 {
		t.Fatalf("parsed %d poetry entries: %+v", len(got), got)
	}
	for name, e := range got {
		if !e.Private {
			t.Errorf("poetry %s was not marked private", name)
		}
	}
}

// A lock that does not parse must not hand back the packages read before
// the damage as public candidates.
func TestMalformedLockYieldsNoCandidates(t *testing.T) {
	for name, tc := range map[string]struct {
		data []byte
		re   *regexp.Regexp
	}{
		"uv": {[]byte(`
version = 1

[[package]]
name = "requests"
version = "2.31.0"
source = { registry = "https://pypi.org/simple" }

[[package]]
name = "internal-sdk"
version = "1.4.0"
source = { registry = "https://artifactory.acmecorp.io/simple"
`), uvPrivateSourceRe},
		"poetry": {[]byte(`
[[package]]
name = "requests"
version = "2.31.0"

[[package]]
name = "internal-sdk"
version = "1.4.0"

[package.source]
type = "legacy"
url = "https://pypi.internal.acmecorp.io/simple
`), poetryPrivateSourceRe},
	} {
		if got := parseLockPackages(tc.data, tc.re); len(got) != 0 {
			t.Errorf("%s: malformed lock produced entries: %+v", name, got)
		}
	}
}
