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
	return domain.SampleManifest{SchemaVersion: 1, Packages: pkgs}
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
			"package-lock.json": `{"packages":{"":{},"node_modules/axios":{"version":"1.12.4"}}}`,
		}, "pkg:npm/axios@1.12.4"},
		{"cargo", "pkg:cargo/serde@1.0.0", map[string]string{
			"Cargo.lock": "[[package]]\nname = \"serde\"\nversion = \"1.0.229\"\n",
		}, "pkg:cargo/serde@1.0.229"},
		{"gem", "pkg:gem/zeitwerk@2.8.0", map[string]string{
			"Gemfile.lock": "GEM\n  specs:\n    zeitwerk (2.8.3)\n",
		}, "pkg:gem/zeitwerk@2.8.3"},
		{"composer", "pkg:composer/symfony/console@7.0.0", map[string]string{
			"composer.lock": `{"packages":[{"name":"symfony/console","version":"v7.4.16"}]}`,
		}, "pkg:composer/symfony/console@7.4.16"},
		{"hex", "pkg:hex/decimal@2.0.0", map[string]string{
			"mix.lock": `%{"decimal": {:hex, :decimal, "2.3.0", "abc", [:mix], [], "hexpm"},}`,
		}, "pkg:hex/decimal@2.3.0"},
		{"golang", "pkg:golang/golang.org/x/net@v0.17.0", map[string]string{
			"go.mod": "module example.com/app\ngo 1.24\nrequire golang.org/x/net v0.38.0\n",
		}, "pkg:golang/golang.org/x/net@v0.38.0"},
		{"pypi", "pkg:pypi/httpx@0.28.0", map[string]string{
			".csx-vendor/py/httpx-0.28.1.dist-info/METADATA": "Name: httpx\n",
		}, "pkg:pypi/httpx@0.28.1"},
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

// A go.mod replace decides what compiled, exactly as it does when the
// scanner reads a user's project.
func TestResolvedPackagesHonoursAGoReplace(t *testing.T) {
	dir := t.TempDir()
	writeAll(t, dir, map[string]string{
		"go.mod": "module example.com/app\ngo 1.24\n" +
			"require golang.org/x/crypto v0.21.0\n" +
			"replace golang.org/x/crypto v0.21.0 => golang.org/x/crypto v0.31.0\n",
	})
	got := resolvedPackages(dir, manifestWith("pkg:golang/golang.org/x/crypto@v0.21.0"))
	if len(got) != 1 || got[0] != "pkg:golang/golang.org/x/crypto@v0.31.0" {
		t.Errorf("resolved %v, want the replace target", got)
	}
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
		"package-lock.json": `{"packages":{"node_modules/axios":{"version":"1.13.0"}}}`,
	})
	got := resolvedPackages(dir, manifestWith("pkg:npm/axios@1.12.0"))
	if len(got) != 1 || got[0] != "pkg:npm/axios@1.13.0" {
		t.Fatalf("resolved %v, want the version that actually installed", got)
	}
}
