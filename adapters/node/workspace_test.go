package node

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

type wantPkg struct{ direct, private bool }

// checkPkgs asserts every wanted name@version is present with its flags and
// that every package names the root lockfile, never a relative path.
func checkPkgs(t *testing.T, pkgs []scanner.ResolvedPackage, source string, want map[string]wantPkg) {
	t.Helper()
	got := map[string]scanner.ResolvedPackage{}
	for _, p := range pkgs {
		got[p.PURL.Name+"@"+p.PURL.Version] = p
		if p.Source != source {
			t.Errorf("%s source = %q, want %q", p.PURL, p.Source, source)
		}
	}
	for key, w := range want {
		p, ok := got[key]
		if !ok {
			t.Errorf("missing %s in %+v", key, pkgs)
			continue
		}
		if p.Direct != w.direct || (p.Publicness == scanner.PublicnessPrivate) != w.private {
			t.Errorf("%s: direct=%v publicness=%s, want direct=%v private=%v", key, p.Direct, p.Publicness, w.direct, w.private)
		}
	}
	assertNoPublic(t, pkgs)
}

func scan(t *testing.T, dir string) []scanner.ResolvedPackage {
	t.Helper()
	pkgs, err := (Adapter{}).ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatalf("ScanPackages(%s): %v", dir, err)
	}
	return pkgs
}

// Every monorepo below has the same shape: a root that declares apps/* and
// packages/* and owns the only lockfile; apps/web uses axios 1.12.0, the
// sibling workspace @repo/ui and a file: path; apps/admin uses lodash and an
// older axios; the root itself uses turbo; tools/script is not a workspace.
func monorepoFiles(rootFiles map[string]string) map[string]string {
	files := map[string]string{
		"apps/web/package.json":     `{"name":"web","dependencies":{"axios":"^1.12.0","@repo/ui":"workspace:*","local-lib":"file:../../vendor/local-lib"}}`,
		"apps/admin/package.json":   `{"name":"admin","dependencies":{"lodash":"^4.17.21","axios":"^0.27.0"}}`,
		"packages/ui/package.json":  `{"name":"@repo/ui","version":"0.0.0"}`,
		"tools/script/package.json": `{"name":"script","dependencies":{"axios":"^1.12.0"}}`,
	}
	maps.Copy(files, rootFiles)
	return files
}

const npmMonorepoLock = `{"name":"mono","lockfileVersion":3,"packages":{
 "":{"name":"mono","workspaces":["apps/*","packages/*"],"devDependencies":{"turbo":"^2.0.0"}},
 "apps/web":{"name":"web","dependencies":{"axios":"^1.12.0","@repo/ui":"*","local-lib":"file:../../vendor/local-lib"}},
 "apps/admin":{"name":"admin","dependencies":{"lodash":"^4.17.21","axios":"^0.27.0"}},
 "packages/ui":{"name":"@repo/ui","version":"0.0.0"},
 "vendor/local-lib":{"name":"local-lib","version":"1.0.0"},
 "node_modules/web":{"resolved":"apps/web","link":true},
 "node_modules/admin":{"resolved":"apps/admin","link":true},
 "node_modules/@repo/ui":{"resolved":"packages/ui","link":true},
 "apps/web/node_modules/local-lib":{"resolved":"vendor/local-lib","link":true},
 "node_modules/turbo":{"version":"2.1.0","dev":true},
 "node_modules/lodash":{"version":"4.17.21"},
 "node_modules/axios":{"version":"0.27.2"},
 "apps/web/node_modules/axios":{"version":"1.12.0"}
}}`

const pnpmMonorepoLock = `lockfileVersion: '9.0'

importers:

  .:
    devDependencies:
      turbo:
        specifier: ^2.0.0
        version: 2.1.0

  apps/admin:
    dependencies:
      axios:
        specifier: ^0.27.0
        version: 0.27.2
      lodash:
        specifier: ^4.17.21
        version: 4.17.21

  apps/web:
    dependencies:
      '@repo/ui':
        specifier: workspace:*
        version: link:../../packages/ui
      axios:
        specifier: ^1.12.0
        version: 1.12.0
      local-lib:
        specifier: file:../../vendor/local-lib
        version: file:vendor/local-lib

  packages/ui: {}

packages:

  axios@0.27.2:
    resolution: {integrity: sha512-a}

  axios@1.12.0:
    resolution: {integrity: sha512-b}

  lodash@4.17.21:
    resolution: {integrity: sha512-c}

  turbo@2.1.0:
    resolution: {integrity: sha512-d}

  local-lib@file:vendor/local-lib:
    resolution: {directory: vendor/local-lib, type: directory}
    name: local-lib
    version: 1.0.0
`

const bunMonorepoLock = `{
  "lockfileVersion": 1,
  "workspaces": {
    "": {"name": "mono", "devDependencies": {"turbo": "^2.0.0"}},
    "apps/admin": {"name": "admin", "dependencies": {"axios": "^0.27.0", "lodash": "^4.17.21"}},
    "apps/web": {"name": "web", "dependencies": {"@repo/ui": "workspace:*", "axios": "^1.12.0", "local-lib": "file:../../vendor/local-lib"}},
    "packages/ui": {"name": "@repo/ui", "version": "0.0.0"},
  },
  "packages": {
    "@repo/ui": ["@repo/ui@workspace:packages/ui"],
    "admin": ["admin@workspace:apps/admin"],
    "web": ["web@workspace:apps/web"],
    "axios": ["axios@0.27.2", "", {}, "sha512-a"],
    "web/axios": ["axios@1.12.0", "", {}, "sha512-b"],
    "lodash": ["lodash@4.17.21", "", {}, "sha512-c"],
    "turbo": ["turbo@2.1.0", "", {}, "sha512-d"],
    "local-lib": ["local-lib@file:vendor/local-lib", {}],
  }
}`

const yarnMonorepoLock = `# yarn lockfile v1


axios@^0.27.0:
  version "0.27.2"
  resolved "https://registry.yarnpkg.com/axios/-/axios-0.27.2.tgz#a"

axios@^1.12.0:
  version "1.12.0"
  resolved "https://registry.yarnpkg.com/axios/-/axios-1.12.0.tgz#b"

lodash@^4.17.21:
  version "4.17.21"
  resolved "https://registry.yarnpkg.com/lodash/-/lodash-4.17.21.tgz#c"

turbo@^2.0.0:
  version "2.1.0"
  resolved "https://registry.yarnpkg.com/turbo/-/turbo-2.1.0.tgz#d"

"local-lib@file:../../vendor/local-lib":
  version "1.0.0"
`

const rootWorkspacesJSON = `{"name":"mono","private":true,"workspaces":["apps/*","packages/*"],"devDependencies":{"turbo":"^2.0.0"}}`

func TestScanPackagesWorkspaceMember(t *testing.T) {
	for _, tc := range []struct {
		name, lockfile string
		files          map[string]string
		member         map[string]wantPkg
		root           map[string]wantPkg
	}{
		{
			name: "npm", lockfile: "package-lock.json",
			files: map[string]string{"package.json": rootWorkspacesJSON, "package-lock.json": npmMonorepoLock},
			member: map[string]wantPkg{
				"axios@1.12.0": {true, false}, "axios@0.27.2": {false, false},
				"lodash@4.17.21": {false, false}, "turbo@2.1.0": {false, false},
				"@repo/ui@0.0.0": {true, true}, "local-lib@1.0.0": {true, true},
			},
			root: map[string]wantPkg{
				"turbo@2.1.0": {true, false}, "axios@0.27.2": {false, false},
				"axios@1.12.0": {false, false}, "lodash@4.17.21": {false, false},
			},
		},
		{
			name: "pnpm", lockfile: "pnpm-lock.yaml",
			files: map[string]string{
				"package.json":        `{"name":"mono","private":true,"devDependencies":{"turbo":"^2.0.0"}}`,
				"pnpm-workspace.yaml": "packages:\n  - 'apps/*' # applications\n  - \"packages/*\"\n",
				"pnpm-lock.yaml":      pnpmMonorepoLock,
			},
			member: map[string]wantPkg{
				"axios@1.12.0": {true, false}, "axios@0.27.2": {false, false},
				"lodash@4.17.21": {false, false}, "turbo@2.1.0": {false, false},
				"@repo/ui@": {true, true}, "local-lib@1.0.0": {true, true},
			},
			root: map[string]wantPkg{
				"turbo@2.1.0": {true, false}, "axios@0.27.2": {false, false},
				"axios@1.12.0": {false, false}, "lodash@4.17.21": {false, false},
			},
		},
		{
			name: "yarn", lockfile: "yarn.lock",
			files: map[string]string{"package.json": rootWorkspacesJSON, "yarn.lock": yarnMonorepoLock},
			member: map[string]wantPkg{
				"axios@1.12.0": {true, false}, "axios@0.27.2": {false, false},
				"lodash@4.17.21": {false, false}, "turbo@2.1.0": {false, false},
				"local-lib@1.0.0": {true, true},
			},
			root: map[string]wantPkg{
				"turbo@2.1.0": {true, false}, "axios@0.27.2": {false, false},
				"axios@1.12.0": {false, false}, "lodash@4.17.21": {false, false},
			},
		},
		{
			name: "bun", lockfile: "bun.lock",
			files: map[string]string{
				"package.json": `{"name":"mono","private":true,"workspaces":{"packages":["apps/*","packages/*"]},"devDependencies":{"turbo":"^2.0.0"}}`,
				"bun.lock":     bunMonorepoLock,
			},
			member: map[string]wantPkg{
				"axios@1.12.0": {true, false}, "axios@0.27.2": {false, false},
				"lodash@4.17.21": {false, false}, "turbo@2.1.0": {false, false},
				"@repo/ui@workspace:packages/ui": {true, true}, "local-lib@file:vendor/local-lib": {true, true},
			},
			root: map[string]wantPkg{
				"turbo@2.1.0": {true, false}, "axios@0.27.2": {false, false},
				"axios@1.12.0": {false, false}, "lodash@4.17.21": {false, false},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, monorepoFiles(tc.files))

			web := filepath.Join(root, "apps", "web")
			checkPkgs(t, scan(t, web), tc.lockfile, tc.member)
			checkPkgs(t, scan(t, root), tc.lockfile, tc.root)

			// A relative directory reaches the same root.
			t.Chdir(root)
			checkPkgs(t, scan(t, filepath.Join("apps", "web")), tc.lockfile, tc.member)

			if pkgs := scan(t, filepath.Join(root, "tools", "script")); len(pkgs) != 0 {
				t.Errorf("undeclared folder borrowed the root lockfile: %+v", pkgs)
			}

			// The member's own lockfile wins over the root's.
			writeTree(t, web, map[string]string{"package-lock.json": `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"own-lib":"^1.0.0"}},
				"node_modules/own-lib":{"version":"1.0.0"}}}`})
			own := scan(t, web)
			if len(own) != 1 || own[0].PURL.Name != "own-lib" || !own[0].Direct || own[0].Source != "package-lock.json" {
				t.Errorf("own lockfile not preferred: %+v", own)
			}
		})
	}
}

func TestScanPackagesWorkspaceBoundaries(t *testing.T) {
	t.Run("negated pattern", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, monorepoFiles(map[string]string{
			"package.json":      `{"workspaces":["apps/*","!apps/web"]}`,
			"package-lock.json": npmMonorepoLock,
		}))
		if pkgs := scan(t, filepath.Join(root, "apps", "web")); len(pkgs) != 0 {
			t.Errorf("excluded member borrowed the root lockfile: %+v", pkgs)
		}
	})
	t.Run("no workspaces", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, monorepoFiles(map[string]string{
			"package.json":      `{"name":"plain"}`,
			"package-lock.json": npmMonorepoLock,
		}))
		if pkgs := scan(t, filepath.Join(root, "apps", "web")); len(pkgs) != 0 {
			t.Errorf("unrelated ancestor lockfile used: %+v", pkgs)
		}
	})
	t.Run("closest lockfile decides", func(t *testing.T) {
		// An ancestor that does declare the folder is never reached past a
		// closer, unrelated lockfile.
		root := t.TempDir()
		writeTree(t, root, map[string]string{
			"package.json":                `{"workspaces":["**"]}`,
			"package-lock.json":           npmMonorepoLock,
			"inner/package.json":          `{"name":"inner"}`,
			"inner/yarn.lock":             yarnMonorepoLock,
			"inner/apps/web/package.json": `{"name":"web","dependencies":{"axios":"^1.12.0"}}`,
		})
		if pkgs := scan(t, filepath.Join(root, "inner", "apps", "web")); len(pkgs) != 0 {
			t.Errorf("climbed past the closest lockfile: %+v", pkgs)
		}
	})
	t.Run("stale lockfile", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, monorepoFiles(map[string]string{
			"package.json":      rootWorkspacesJSON,
			"package-lock.json": `{"lockfileVersion":3,"packages":{"":{},"node_modules/axios":{"version":"1.12.0"}}}`,
		}))
		if pkgs := scan(t, filepath.Join(root, "apps", "web")); len(pkgs) != 0 {
			t.Errorf("a lockfile without the member must not describe it: %+v", pkgs)
		}
	})
}

func TestWorkspaceDeclares(t *testing.T) {
	for _, tt := range []struct {
		patterns []string
		rel      string
		want     bool
	}{
		{[]string{"apps/*"}, "apps/web", true},
		{[]string{"./apps/*/"}, "apps/web", true},
		{[]string{"apps/web"}, "apps/web", true},
		{[]string{"apps/*"}, "apps/web/src", false},
		{[]string{"packages/**"}, "packages/a/b", true},
		{[]string{"**"}, "apps/web", true},
		{[]string{"apps/*", "!apps/legacy"}, "apps/legacy", false},
		{[]string{"../*"}, "apps", false},
		{nil, "apps/web", false},
	} {
		if got := workspaceDeclares(tt.patterns, tt.rel); got != tt.want {
			t.Errorf("workspaceDeclares(%q, %q) = %v, want %v", tt.patterns, tt.rel, got, tt.want)
		}
	}
}

func TestPnpmWorkspacePatternsFlowForm(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"pnpm-workspace.yaml": "# workspace\npackages: ['apps/*', \"packages/*\"]\ncatalog:\n  - nope\n"})
	got := pnpmWorkspacePatterns(dir)
	if len(got) != 2 || got[0] != "apps/*" || got[1] != "packages/*" {
		t.Errorf("patterns = %q", got)
	}
}

func TestEnvironmentHintsWorkspaceMember(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, monorepoFiles(map[string]string{
		"package.json":        `{"name":"mono","packageManager":"pnpm@9.1.0+sha512.abc"}`,
		"pnpm-workspace.yaml": "packages:\n  - apps/*\n",
		"pnpm-lock.yaml":      pnpmMonorepoLock,
	}))
	ctx := context.Background()
	h := (Adapter{}).EnvironmentHints(ctx, filepath.Join(root, "apps", "web"))
	if h["packageManager"] != "pnpm" || h["packageManagerVersion"] != "9.1.0" {
		t.Errorf("member hints = %v, want pnpm 9.1.0 from the workspace root", h)
	}
	if h := (Adapter{}).EnvironmentHints(ctx, filepath.Join(root, "tools", "script")); h["packageManager"] != "" {
		t.Errorf("undeclared folder took the root's package manager: %v", h)
	}

	// The member's own clue wins.
	writeTree(t, root, map[string]string{"apps/web/package.json": `{"name":"web","packageManager":"yarn@1.22.22"}`})
	h = (Adapter{}).EnvironmentHints(ctx, filepath.Join(root, "apps", "web"))
	if h["packageManager"] != "yarn" || h["packageManagerVersion"] != "1.22.22" {
		t.Errorf("member's own packageManager not preferred: %v", h)
	}
}
