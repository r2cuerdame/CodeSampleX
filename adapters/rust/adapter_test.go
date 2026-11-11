package rust

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "simple"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestEcosystemAndCapabilities(t *testing.T) {
	var a scanner.Adapter = New()
	if got := a.Ecosystem(); got != "cargo" {
		t.Errorf("Ecosystem() = %q, want %q", got, "cargo")
	}
	caps := a.Capabilities()
	want := []string{"A0", "A1", "A2"}
	if len(caps) != len(want) {
		t.Fatalf("Capabilities() = %v, want %v", caps, want)
	}
	for i := range want {
		if caps[i] != want[i] {
			t.Errorf("Capabilities()[%d] = %q, want %q", i, caps[i], want[i])
		}
	}
}

func TestDetect(t *testing.T) {
	a := New()
	if !a.Detect(fixtureDir(t)) {
		t.Error("Detect(fixture) = false, want true")
	}
	if a.Detect(t.TempDir()) {
		t.Error("Detect(empty dir) = true, want false")
	}
}

func TestScanPackages(t *testing.T) {
	a := New()
	pkgs, err := a.ScanPackages(context.Background(), fixtureDir(t))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]scanner.ResolvedPackage{}
	for _, p := range pkgs {
		if p.PURL.Ecosystem != "cargo" {
			t.Errorf("%s: ecosystem = %q, want cargo", p.PURL.Name, p.PURL.Ecosystem)
		}
		if p.Source != "Cargo.lock" {
			t.Errorf("%s: source = %q, want Cargo.lock", p.PURL.Name, p.Source)
		}
		byName[p.PURL.Name] = p
	}
	if len(pkgs) != 5 {
		t.Errorf("got %d packages, want 5: %v", len(pkgs), byName)
	}
	if _, ok := byName["fixture-app"]; ok {
		t.Error("root package fixture-app must not be reported")
	}
	tests := []struct {
		name, version, publicness string
		direct                    bool
	}{
		{"serde", "1.0.219", scanner.PublicnessUnknown, true},
		{"serde_derive", "1.0.219", scanner.PublicnessUnknown, false},
		{"serde_json", "1.0.140", scanner.PublicnessUnknown, true},
		{"local-helper", "0.1.0", scanner.PublicnessPrivate, true},
		{"gitdep", "0.2.0", scanner.PublicnessPrivate, true},
	}
	for _, tt := range tests {
		p, ok := byName[tt.name]
		if !ok {
			t.Errorf("missing package %q", tt.name)
			continue
		}
		if p.PURL.Version != tt.version {
			t.Errorf("%s: version = %q, want %q", tt.name, p.PURL.Version, tt.version)
		}
		if p.Publicness != tt.publicness {
			t.Errorf("%s: publicness = %q, want %q", tt.name, p.Publicness, tt.publicness)
		}
		if p.Direct != tt.direct {
			t.Errorf("%s: direct = %v, want %v", tt.name, p.Direct, tt.direct)
		}
	}
}

// A dependency declared only for one target, or under a different name, is
// still one the developer chose. Cargo.lock knows a renamed crate by its real
// name only, so the manifest has to be read for that name too; the key it was
// declared under can belong to some other crate in the tree.
func TestScanPackagesTargetAndRenamedDependencies(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[package]
name = "app"
version = "0.1.0"

[dependencies]
serde = "1.0"
json = { package = "serde_json", version = "1.0" }
helper = { package = "local-helper", path = "../local-helper" }
rt.package = "tokio"
rt.version = "1"

[dependencies.vcs]
git = "https://example.invalid/vcs-real.git"
package = "vcs-real"

[target.'cfg(windows)'.dependencies.winapi]
version = "0.3"

[target.x86_64-unknown-linux-gnu.dependencies.libc]
version = "0.2"

[target."cfg(target_os = \"macos\")".build-dependencies.cc]
version = "1"

[target.'cfg(unix)'.dev-dependencies]
nix = "0.29"
`)
	const crates = `source = "registry+https://github.com/rust-lang/crates.io-index"`
	writeLock(t, dir, `
version = 4

[[package]]
name = "app"
version = "0.1.0"

[[package]]
name = "cc"
version = "1.2.27"
`+crates+`

[[package]]
name = "json"
version = "0.12.4"
`+crates+`

[[package]]
name = "libc"
version = "0.2.174"
`+crates+`

[[package]]
name = "local-helper"
version = "0.1.0"

[[package]]
name = "local-helper"
version = "0.0.9"
`+crates+`

[[package]]
name = "nix"
version = "0.29.0"
source = "sparse+https://index.crates.io/"

[[package]]
name = "serde"
version = "1.0.219"
`+crates+`

[[package]]
name = "serde_derive"
version = "1.0.219"
`+crates+`

[[package]]
name = "serde_json"
version = "1.0.140"
`+crates+`

[[package]]
name = "tokio"
version = "1.47.1"
`+crates+`

[[package]]
name = "vcs-real"
version = "0.3.0"
source = "git+https://example.invalid/vcs-real.git#0000000000000000000000000000000000000000"

[[package]]
name = "vcs-real"
version = "0.2.0"
`+crates+`

[[package]]
name = "winapi"
version = "0.3.9"
`+crates+`
`)

	pkgs, err := New().ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]scanner.ResolvedPackage{}
	for _, p := range pkgs {
		got[p.PURL.Name+"@"+p.PURL.Version] = p
	}
	want := map[string]struct {
		publicness string
		direct     bool
	}{
		// Target-specific [target.<cfg or triple>.<kind>.<name>] tables,
		// however the target key is quoted.
		"winapi@0.3.9":  {scanner.PublicnessUnknown, true},
		"libc@0.2.174":  {scanner.PublicnessUnknown, true},
		"cc@1.2.27":     {scanner.PublicnessUnknown, true},
		"nix@0.29.0":    {scanner.PublicnessUnknown, true},
		"serde@1.0.219": {scanner.PublicnessUnknown, true},
		// Renamed: inline table, dotted keys, and a [dependencies.<alias>] table.
		"serde_json@1.0.140": {scanner.PublicnessUnknown, true},
		"tokio@1.47.1":       {scanner.PublicnessUnknown, true},
		"vcs-real@0.3.0":     {scanner.PublicnessPrivate, true},
		// A path/git dependency's name is private wherever it appears, so the
		// crates.io package sharing a renamed path/git crate's real name is too.
		"local-helper@0.1.0": {scanner.PublicnessPrivate, true},
		"local-helper@0.0.9": {scanner.PublicnessPrivate, true},
		"vcs-real@0.2.0":     {scanner.PublicnessPrivate, true},
		// "json" is only an alias in this manifest; the crate of that name is
		// somebody else's dependency.
		"json@0.12.4":          {scanner.PublicnessUnknown, false},
		"serde_derive@1.0.219": {scanner.PublicnessUnknown, false},
	}
	if len(pkgs) != len(want) {
		t.Errorf("got %d packages, want %d: %v", len(pkgs), len(want), got)
	}
	for key, w := range want {
		p, ok := got[key]
		if !ok {
			t.Errorf("missing package %s", key)
			continue
		}
		if p.Publicness != w.publicness {
			t.Errorf("%s: publicness = %q, want %q", key, p.Publicness, w.publicness)
		}
		if p.Direct != w.direct {
			t.Errorf("%s: direct = %v, want %v", key, p.Direct, w.direct)
		}
	}
}

// Reading target tables by their last parts must not turn tables that are not
// dependencies into dependencies.
func TestDepSection(t *testing.T) {
	tests := []struct {
		sec    string
		list   bool
		single string
	}{
		{"dependencies", true, ""},
		{"dev-dependencies", true, ""},
		{"target.'cfg(unix)'.build-dependencies", true, ""},
		{"dependencies.serde", false, "serde"},
		{`dependencies."serde"`, false, "serde"},
		{"target.'cfg(target_os = \"linux\")'.dependencies.libc", false, "libc"},
		{`target."cfg(target_os = \"linux\")".dependencies.libc`, false, "libc"},
		{"target.'custom.json'.dev-dependencies.foo", false, "foo"},
		{"package", false, ""},
		{"features", false, ""},
		{"patch.crates-io", false, ""},
		{"patch.crates-io.serde", false, ""},
		{"package.metadata.docs.rs", false, ""},
		{"dependencies.serde.features", false, ""},
		{"target.'cfg(unix)'", false, ""},
		// One-dependency tables declare the root package's dependency only at
		// the root or under target.<selector>.
		{"workspace.dependencies.serde", false, ""},
		{"package.metadata.audit.dependencies.serde", false, ""},
		{"target.'cfg(unix)'.metadata.dependencies.serde", false, ""},
		{"target.dependencies.serde", false, ""},
		{"build.dependencies.serde", false, ""},
	}
	for _, tt := range tests {
		list, single := depSection(tt.sec)
		if list != tt.list || single != tt.single {
			t.Errorf("depSection(%q) = (%v, %q), want (%v, %q)", tt.sec, list, single, tt.list, tt.single)
		}
	}
}

// A dependency table that is not the root package's, and a field named only
// in a comment or a string, must not decide which crate is direct or private.
func TestScanPackagesIgnoresForeignTablesAndComments(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[package]
name = "app"
version = "0.1.0"

[dependencies]
serde = "1" # previously package = "serde_json"
anyhow = { version = "1", features = ["std"] } # was path = "../anyhow"
log = { version = "0.4", features = ["package = \"tokio\"", "git = x"] }

[workspace.dependencies.serde_json]
version = "1"

[package.metadata.audit.dependencies.tokio]
path = "../tokio"
`)
	const crates = `source = "registry+https://github.com/rust-lang/crates.io-index"`
	writeLock(t, dir, `
version = 4

[[package]]
name = "anyhow"
version = "1.0.98"
`+crates+`

[[package]]
name = "app"
version = "0.1.0"

[[package]]
name = "log"
version = "0.4.27"
`+crates+`

[[package]]
name = "serde"
version = "1.0.219"
`+crates+`

[[package]]
name = "serde_json"
version = "1.0.140"
`+crates+`

[[package]]
name = "tokio"
version = "1.47.1"
`+crates+`
`)

	pkgs, err := New().ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]scanner.ResolvedPackage{}
	for _, p := range pkgs {
		got[p.PURL.Name] = p
	}
	want := map[string]bool{ // direct; every crate stays a public candidate
		"serde":      true,
		"anyhow":     true,
		"log":        true,
		"serde_json": false,
		"tokio":      false,
	}
	if len(pkgs) != len(want) {
		t.Errorf("got %d packages, want %d: %v", len(pkgs), len(want), got)
	}
	for name, direct := range want {
		p, ok := got[name]
		if !ok {
			t.Errorf("missing package %s", name)
			continue
		}
		if p.Publicness != scanner.PublicnessUnknown {
			t.Errorf("%s: publicness = %q, want %q", name, p.Publicness, scanner.PublicnessUnknown)
		}
		if p.Direct != direct {
			t.Errorf("%s: direct = %v, want %v", name, p.Direct, direct)
		}
	}
}

func TestInlineTableFields(t *testing.T) {
	tests := []struct {
		value string
		want  map[string]string
	}{
		{`"1.0"`, nil},
		{`"1" # previously package = "serde_json"`, nil},
		{`{ package = "serde_json", version = "1.0" }`, map[string]string{"package": `"serde_json"`, "version": `"1.0"`}},
		{`{path="../x"}`, map[string]string{"path": `"../x"`}},
		{`{ "package" = 'real', git = "https://h.invalid/x?path=1" }`, map[string]string{"package": `'real'`, "git": `"https://h.invalid/x?path=1"`}},
		{`{ version = "1" } # package = "other"`, map[string]string{"version": `"1"`}},
		{`{ version = "1", features = ["package = x", "path"] }`, map[string]string{"version": `"1"`, "features": `["package = x", "path"]`}},
		{`{ version = "1", registry = "a\"b, package = c" }`, map[string]string{"version": `"1"`, "registry": `"a\"b, package = c"`}},
		{`{ version = "1", features = [ # derive below`, map[string]string{"version": `"1"`, "features": `[`}},
		{`{ path = "../x", features = [`, map[string]string{"path": `"../x"`, "features": `[`}},
	}
	for _, tt := range tests {
		got := inlineTableFields(tt.value)
		if (got == nil) != (tt.want == nil) || len(got) != len(tt.want) {
			t.Errorf("inlineTableFields(%s) = %v, want %v", tt.value, got, tt.want)
			continue
		}
		for k, v := range tt.want {
			if got[k] != v {
				t.Errorf("inlineTableFields(%s)[%q] = %q, want %q", tt.value, k, got[k], v)
			}
		}
	}
}

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanPackagesNoLockfile(t *testing.T) {
	a := New()
	pkgs, err := a.ScanPackages(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 0 {
		t.Errorf("got %d packages without a lockfile, want 0", len(pkgs))
	}
}

func TestClassifyCommand(t *testing.T) {
	a := New()
	tests := []struct {
		argv  []string
		stage domain.Stage
		known bool
	}{
		{[]string{"cargo", "build", "--release"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "check"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "clippy"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "+nightly", "build"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "test"}, domain.StageProjectTest, true},
		{[]string{"cargo", "nextest"}, domain.StageProjectTest, true},
		{[]string{"cargo", "nextest", "run"}, domain.StageProjectTest, true},
		{[]string{"cargo-nextest", "run"}, domain.StageProjectTest, true},
		{[]string{"cargo", "bench"}, domain.StageProjectTest, true},
		{[]string{"cargo", "--manifest-path", "Cargo.toml", "test"}, domain.StageProjectTest, true},
		{[]string{"cargo", "--target", "x86_64-unknown-linux-gnu", "build"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "--target-dir", "target/custom", "--config", "net.offline=true", "--profile", "ci", "-p", "my-crate", "-Z", "unstable-options", "-C", ".", "nextest", "run"}, domain.StageProjectTest, true},
		{[]string{`C:\tools\cargo.EXE`, "test"}, domain.StageProjectTest, true},
		{[]string{"cargo", "run"}, domain.StageProjectProcess, true},
		{[]string{"rustc", "main.rs"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "fmt"}, "", false},
		{[]string{"npm", "test"}, "", false},
		{nil, "", false},
	}
	for _, tt := range tests {
		got := a.ClassifyCommand(tt.argv)
		if got.Known != tt.known {
			t.Errorf("ClassifyCommand(%v).Known = %v, want %v", tt.argv, got.Known, tt.known)
			continue
		}
		if tt.known && got.Stage != tt.stage {
			t.Errorf("ClassifyCommand(%v).Stage = %q, want %q", tt.argv, got.Stage, tt.stage)
		}
	}
}

func TestEnvironmentHints(t *testing.T) {
	a := New()
	hints := a.EnvironmentHints(context.Background(), fixtureDir(t))
	want := map[string]string{
		"ecosystem":      "cargo",
		"runtime":        "",
		"language":       "rust",
		"packageManager": "cargo",
		"moduleSystem":   "",
	}
	if len(hints) != len(want) {
		t.Errorf("hints = %v, want %v", hints, want)
	}
	for k, v := range want {
		got, ok := hints[k]
		if !ok || got != v {
			t.Errorf("hints[%q] = %q (present=%v), want %q", k, got, ok, v)
		}
	}
}
