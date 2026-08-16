package verifier

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func writeAll(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func manifestWith(pkgs ...string) domain.SampleManifest {
	m := domain.SampleManifest{SchemaVersion: 1, Packages: pkgs}
	if len(pkgs) > 0 {
		if p, err := domain.ParsePURL(pkgs[0]); err == nil {
			m.Environment.Ecosystem = p.Ecosystem
		}
	}
	return m
}

func manifestFor(runtime, manager string, pkgs ...string) domain.SampleManifest {
	m := manifestWith(pkgs...)
	m.Environment.Runtime = runtime
	m.Environment.PackageManager = manager
	return m
}

// The lockfile the resolve stage generated is the only evidence of what
// actually ran. The manifest is the author's claim about it.
func TestResolvedPackagesReadsEachEcosystemsLockfile(t *testing.T) {
	cases := []struct {
		name  string
		purl  string
		files map[string]string
		want  string
	}{
		{"npm", "pkg:npm/axios@1.12.0", map[string]string{
			"package-lock.json":               `{"packages":{"":{},"node_modules/axios":{"version":"1.12.4","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.4.tgz"}}}`,
			"node_modules/axios/package.json": `{"name":"axios","version":"1.12.4"}`,
		}, "pkg:npm/axios@1.12.4"},
		{"cargo", "pkg:cargo/serde@1.0.0", map[string]string{
			"Cargo.lock": "[[package]]\nname = \"serde\"\nversion = \"1.0.229\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\n",
			".csx-vendor/cargo/registry/cache/index.crates.io-abc/serde-1.0.229.crate": "fetched",
		}, "pkg:cargo/serde@1.0.229"},
		{"gem", "pkg:gem/zeitwerk@2.8.0", map[string]string{
			"Gemfile.lock": "GEM\n  remote: https://rubygems.org/\n  specs:\n    zeitwerk (2.8.3)\n",
			".csx-vendor/gems/ruby/3.4.0/specifications/zeitwerk-2.8.3.gemspec": "Gem::Specification.new do |s|\n  s.name = \"zeitwerk\".freeze\n  s.version = \"2.8.3\".freeze\nend\n",
		}, "pkg:gem/zeitwerk@2.8.3"},
		{"hex", "pkg:hex/decimal@2.0.0", map[string]string{
			"mix.lock":             `%{"decimal": {:hex, :decimal, "2.3.0", "abc", [:mix], [], "hexpm"},}`,
			"deps/decimal/mix.exs": "defmodule Decimal.MixProject do\nend\n",
		}, "pkg:hex/decimal@2.3.0"},
		{"golang", "pkg:golang/golang.org/x/net@v0.17.0", map[string]string{
			".csx-vendor/go-modules.json": `{"Path":"example.com/app","Main":true}
{"Path":"golang.org/x/net","Version":"v0.38.0"}
`,
		}, "pkg:golang/golang.org/x/net@v0.38.0"},
		{"pypi", "pkg:pypi/httpx@0.28.0", map[string]string{
			".csx-vendor/py/httpx-0.28.1.dist-info/METADATA": "Metadata-Version: 2.4\nName: httpx\nVersion: 0.28.1\n\nbody\n",
			".csx-vendor/pip-report.json":                    `{"install":[{"download_info":{"url":"https://files.pythonhosted.org/packages/aa/httpx-0.28.1.whl","archive_info":{"hash":"sha256=abc"}},"is_direct":false,"metadata":{"name":"httpx","version":"0.28.1"}}]}`,
		}, "pkg:pypi/httpx@0.28.1"},
		{"pub", "pkg:pub/collection@1.18.0", map[string]string{
			"pubspec.lock":                   "packages:\r\n  collection:\r\n    dependency: direct main\r\n    description:\r\n      name: collection\r\n      url: https://pub.dev\r\n    source: hosted\r\n    version: \"1.19.1\"\r\n",
			".dart_tool/package_config.json": `{"configVersion":2,"packages":[{"name":"collection","rootUri":"file:///work/.csx-vendor/pub/hosted/pub.dev/collection-1.19.1","packageUri":"lib/"}]}`,
		}, "pkg:pub/collection@1.19.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAll(t, dir, c.files)
			got := resolvedPackages(dir, manifestWith(c.purl))
			if len(got) != 1 || got[0] != c.want {
				t.Errorf("resolved %v, want [%s]", got, c.want)
			}
		})
	}
}

func TestResolvedPackagesUsesTheRuntimeLockfile(t *testing.T) {
	t.Run("bun text lock", func(t *testing.T) {
		dir := t.TempDir()
		writeAll(t, dir, map[string]string{
			"bun.lock": `{
  "workspaces": {"": {"dependencies": {"@scope/pkg": "2.4.1",},},},
  "packages": {"@scope/pkg": ["@scope/pkg@2.4.1", "https://registry.npmjs.org/@scope%2fpkg/-/pkg-2.4.1.tgz", {}, "sha512-x"],},
}`,
			// A decoy npm lock must not be credited to a Bun resolve.
			"package-lock.json":                    `{"packages":{"node_modules/@scope/pkg":{"version":"9.9.9"}}}`,
			"node_modules/@scope/pkg/package.json": `{"name":"@scope/pkg","version":"2.4.1"}`,
		})
		got := resolvedPackages(dir, manifestFor("bun", "bun", "pkg:npm/%40scope/pkg@2.0.0"))
		if len(got) != 1 || got[0] != "pkg:npm/%40scope/pkg@2.4.1" {
			t.Fatalf("resolved %v from bun.lock", got)
		}
	})

	t.Run("node ignores a bun decoy", func(t *testing.T) {
		dir := t.TempDir()
		writeAll(t, dir, map[string]string{
			"bun.lock":                        `{"packages":{"axios":["axios@9.9.9","",{},""]}}`,
			"package-lock.json":               `{"packages":{"node_modules/axios":{"version":"1.12.4","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.4.tgz"}}}`,
			"node_modules/axios/package.json": `{"name":"axios","version":"1.12.4"}`,
		})
		got := resolvedPackages(dir, manifestFor("node", "bun", "pkg:npm/axios@1.0.0"))
		if len(got) != 1 || got[0] != "pkg:npm/axios@1.12.4" {
			t.Fatalf("resolved %v, want the lockfile selected by the runner", got)
		}
	})

	t.Run("deno v4 lock", func(t *testing.T) {
		dir := t.TempDir()
		writeAll(t, dir, map[string]string{
			"deno.lock": `{"version":"4","specifiers":{"npm:axios@^1.11.0":"1.12.4"},"npm":{"axios@1.12.4":{"resolved":"https://registry.npmjs.org/axios/-/axios-1.12.4.tgz"}}}`,
		})
		got := resolvedPackages(dir, manifestFor("deno", "deno", "pkg:npm/axios@1.11.0"))
		if len(got) != 1 || got[0] != "pkg:npm/axios@1.12.4" {
			t.Fatalf("resolved %v from deno v4 lock", got)
		}
	})

	t.Run("deno v3 lock", func(t *testing.T) {
		dir := t.TempDir()
		writeAll(t, dir, map[string]string{
			"deno.lock": `{"version":"3","packages":{"specifiers":{"npm:axios@^1.11.0":"npm:axios@1.12.3"},"npm":{"axios@1.12.3":{"registry":"https://registry.npmjs.org/"}}}}`,
		})
		got := resolvedPackages(dir, manifestFor("deno", "deno", "pkg:npm/axios@1.11.0"))
		if len(got) != 1 || got[0] != "pkg:npm/axios@1.12.3" {
			t.Fatalf("resolved %v from deno v3 lock", got)
		}
	})
}

func TestCargoDuplicateVersionsStayUnestablished(t *testing.T) {
	dir := t.TempDir()
	writeAll(t, dir, map[string]string{
		"Cargo.lock": "[[package]]\nname = \"serde\"\nversion = \"1.0.200\"\nsource = \"sparse+https://index.crates.io/\"\n" +
			"[[package]]\nname = \"serde\"\nversion = \"1.0.229\"\nsource = \"sparse+https://index.crates.io/\"\n",
	})
	if got := resolvedPackages(dir, manifestWith("pkg:cargo/serde@1.0.0")); len(got) != 0 {
		t.Fatalf("ambiguous Cargo.lock produced %v", got)
	}
}

func TestCargoSourceReplacementStaysUnestablished(t *testing.T) {
	dir := t.TempDir()
	writeAll(t, dir, map[string]string{
		"Cargo.lock":         "[[package]]\nname = \"serde\"\nversion = \"1.0.229\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\n",
		".cargo/config.toml": "[source.crates-io]\nreplace-with = \"vendored\"\n[source.vendored]\ndirectory = \"vendor\"\n",
	})
	if got := resolvedPackages(dir, manifestWith("pkg:cargo/serde@1.0.0")); len(got) != 0 {
		t.Fatalf("crates.io replacement was credited as public Cargo: %v", got)
	}
}

func TestComposerStaysUnestablishedWithoutIndependentPackagistEvidence(t *testing.T) {
	dir := t.TempDir()
	writeAll(t, dir, map[string]string{
		"composer.lock": `{"packages":[{"name":"symfony/console","version":"v7.4.16","notification-url":"https://packagist.org/downloads/","dist":{"type":"zip","url":"https://private.example/forged.zip"}}]}`,
	})
	if got := resolvedPackages(dir, manifestWith("pkg:composer/symfony/console@7.0.0")); len(got) != 0 {
		t.Fatalf("author-controlled Composer metadata was credited as Packagist: %v", got)
	}
}

func TestResolvedPackagesReadsGoMVSBuildList(t *testing.T) {
	dir := t.TempDir()
	writeAll(t, dir, map[string]string{
		".csx-vendor/go-modules.json": `{"Path":"example.com/app","Main":true}
{"Path":"golang.org/x/crypto","Version":"v0.31.0"}
`,
	})
	// The manifest and go.mod can both request an older version; the persisted
	// build list is the MVS result that actually ran.
	got := resolvedPackages(dir, manifestWith("pkg:golang/golang.org/x/crypto@v0.19.0"))
	if len(got) != 1 || got[0] != "pkg:golang/golang.org/x/crypto@v0.31.0" {
		t.Errorf("resolved %v, want the selected build-list version", got)
	}
}

func TestResolvedPackagesReadsGoReplacementFromBuildList(t *testing.T) {
	t.Run("same module identity", func(t *testing.T) {
		dir := t.TempDir()
		writeAll(t, dir, map[string]string{
			".csx-vendor/go-modules.json": `{"Path":"golang.org/x/crypto","Version":"v0.21.0","Replace":{"Path":"golang.org/x/crypto","Version":"v0.31.0"}}
`,
		})
		got := resolvedPackages(dir, manifestWith("pkg:golang/golang.org/x/crypto@v0.21.0"))
		if len(got) != 1 || got[0] != "pkg:golang/golang.org/x/crypto@v0.31.0" {
			t.Fatalf("resolved %v, want build-list replacement", got)
		}
	})

	t.Run("local replacement", func(t *testing.T) {
		dir := t.TempDir()
		writeAll(t, dir, map[string]string{
			".csx-vendor/go-modules.json": `{"Path":"golang.org/x/crypto","Version":"v0.21.0","Replace":{"Path":"../crypto","Dir":"../crypto"}}
`,
		})
		if got := resolvedPackages(dir, manifestWith("pkg:golang/golang.org/x/crypto@v0.21.0")); len(got) != 0 {
			t.Fatalf("local replacement claimed a registry version: %v", got)
		}
	})

	t.Run("identity-changing replacement", func(t *testing.T) {
		dir := t.TempDir()
		writeAll(t, dir, map[string]string{
			".csx-vendor/go-modules.json": `{"Path":"example.com/original","Version":"v1.2.0","Replace":{"Path":"example.com/fork","Version":"v1.3.0"}}
`,
		})
		if got := resolvedPackages(dir, manifestWith("pkg:golang/example.com/original@v1.2.0")); len(got) != 0 {
			t.Fatalf("identity-changing replacement was reconstructed as the original package: %v", got)
		}
	})
}

// Silence, not a guess. An unreadable lockfile must yield nothing, because
// an empty list means "not established" and a wrong one means the receipt
// asserts a version that never ran.
func TestResolvedPackagesSaysNothingWhenItCannotTell(t *testing.T) {
	dir := t.TempDir()
	if got := resolvedPackages(dir, manifestWith("pkg:npm/axios@1.12.0")); len(got) != 0 {
		t.Errorf("resolved %v from an empty workspace", got)
	}
	writeAll(t, dir, map[string]string{"package-lock.json": "{not json"})
	if got := resolvedPackages(dir, manifestWith("pkg:npm/axios@1.12.0")); len(got) != 0 {
		t.Errorf("resolved %v from an unparseable lockfile", got)
	}
}

// The point of the field: it records what ran, which may differ from what
// the author declared. That difference is the finding, not an error.
func TestResolvedPackagesRecordsADifferenceFromTheManifest(t *testing.T) {
	dir := t.TempDir()
	writeAll(t, dir, map[string]string{
		"package-lock.json":               `{"packages":{"node_modules/axios":{"version":"1.13.0","resolved":"https://registry.npmjs.org/axios/-/axios-1.13.0.tgz"}}}`,
		"node_modules/axios/package.json": `{"name":"axios","version":"1.13.0"}`,
	})
	got := resolvedPackages(dir, manifestWith("pkg:npm/axios@1.12.0"))
	if len(got) != 1 || got[0] != "pkg:npm/axios@1.13.0" {
		t.Fatalf("resolved %v, want the version that actually installed", got)
	}
}

func TestResolvedPackagesIgnoresLockfilesForResolversThatDidNotRun(t *testing.T) {
	dir := t.TempDir()
	writeAll(t, dir, map[string]string{
		"package-lock.json":               `{"packages":{"node_modules/axios":{"version":"1.13.0","resolved":"https://registry.npmjs.org/axios/-/axios-1.13.0.tgz"}}}`,
		"node_modules/axios/package.json": `{"name":"axios","version":"1.13.0"}`,
		"Cargo.lock":                      "[[package]]\nname = \"serde\"\nversion = \"1.0.229\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\n",
	})
	m := manifestWith("pkg:npm/axios@1.12.0", "pkg:cargo/serde@1.0.0")
	got := resolvedPackages(dir, m)
	if len(got) != 1 || got[0] != "pkg:npm/axios@1.13.0" {
		t.Fatalf("npm resolve credited another ecosystem's lockfile: %v", got)
	}
}

func TestNPMResolvedRequiresThePackageToBeInstalled(t *testing.T) {
	lock := `{"packages":{"node_modules/axios":{"version":"1.12.4","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.4.tgz"}}}`
	for _, tc := range []struct {
		name     string
		metadata string
	}{
		{name: "absent"},
		{name: "wrong identity", metadata: `{"name":"not-axios","version":"1.12.4"}`},
		{name: "wrong version", metadata: `{"name":"axios","version":"9.9.9"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{"package-lock.json": lock}
			if tc.metadata != "" {
				files["node_modules/axios/package.json"] = tc.metadata
			}
			writeAll(t, dir, files)
			if got := resolvedPackages(dir, manifestWith("pkg:npm/axios@1.12.0")); len(got) != 0 {
				t.Fatalf("uninstalled package was credited: %v", got)
			}
		})
	}
}
