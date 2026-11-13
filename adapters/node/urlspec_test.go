package node

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func TestIsURLSpec(t *testing.T) {
	for _, s := range []string{
		"https://example.com/foo-1.2.3.tgz",
		"http://example.com/foo-1.2.3.tgz",
		"  HTTPS://Example.com/foo-1.2.3.tgz  ",
		"Http://example.com/foo.tgz#sha1",
	} {
		if !isURLSpec(s) {
			t.Errorf("isURLSpec(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"", "^1.2.3", "1.2.3", "*", "latest", "npm:foo@1.2.3",
		"git+https://github.com/a/b.git", "file:../foo", "github:a/b",
		"https-proxy-agent", "http",
	} {
		if isURLSpec(s) {
			t.Errorf("isURLSpec(%q) = true, want false", s)
		}
	}
}

// tempTree writes files under a fresh temporary directory and returns it.
func tempTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, files)
	return root
}

// allPublicRegistry answers 200 for every npm probe, so any package handed
// to it comes back PUBLIC. Only the adapter's own marking can keep a package
// out, which is the property under test. It records the probed paths.
func allPublicRegistry(t *testing.T) (*registry.Checker, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := &registry.Checker{HTTP: srv.Client(), BaseURLs: map[string]string{"npm": srv.URL}}
	return c, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// assertPublicness checks the publicness of every package after the registry
// check, keyed name@version, and that no package expected to stay private
// was ever sent to the registry.
func assertPublicness(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	pkgs, err := (Adapter{}).ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPublic(t, pkgs)
	checker, probed := allPublicRegistry(t)
	checker.CheckAll(context.Background(), pkgs)
	got := map[string]string{}
	for _, p := range pkgs {
		got[p.PURL.Name+"@"+p.PURL.Version] = p.Publicness
	}
	for key, w := range want {
		if got[key] != w {
			t.Errorf("%s: publicness %q, want %q (all: %v)", key, got[key], w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("packages = %v, want exactly %v", got, want)
	}
	publicName := map[string]bool{}
	for _, p := range pkgs {
		if p.Publicness == scanner.PublicnessPublic {
			publicName[p.PURL.Name] = true
		}
	}
	for _, path := range probed() {
		for _, p := range pkgs {
			if p.Publicness == scanner.PublicnessPublic {
				continue
			}
			if path == "/"+p.PURL.Name+"/"+p.PURL.Version || (path == "/"+p.PURL.Name && !publicName[p.PURL.Name]) {
				t.Errorf("registry was asked about %s (%s), which must never leave the machine", p.PURL, path)
			}
		}
	}
}

const (
	pub  = scanner.PublicnessPublic
	priv = scanner.PublicnessPrivate
	unk  = scanner.PublicnessUnknown
)

// A package-lock records a URL tarball's own name and version, the same
// shape as a registry install. The specifier of whoever asked for it is the
// only difference, whether that is the project, a dependency or a member.
func TestPackageLockURLSpecIsPrivate(t *testing.T) {
	const lock = `{"name":"app","lockfileVersion":3,"packages":{
 "":{"name":"app","dependencies":{"foo":"https://example.com/foo-1.2.3.tgz","axios":"^1.12.0","bar":"^2.1.0"},
     "devDependencies":{"dev-url":" HTTPS://example.com/dev-url-0.1.0.tgz "}},
 "node_modules/foo":{"version":"1.2.3","resolved":"https://example.com/foo-1.2.3.tgz"},
 "node_modules/dev-url":{"version":"0.1.0","resolved":"https://example.com/dev-url-0.1.0.tgz","dev":true},
 "node_modules/axios":{"version":"1.12.0","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.0.tgz",
   "dependencies":{"bar":"https://example.com/bar-2.0.0.tgz","qux":"http://example.com/qux-3.0.0.tgz","follow-redirects":"^1.15.6"},
   "optionalDependencies":{"opt-url":"https://example.com/opt-url-4.0.0.tgz"}},
 "node_modules/axios/node_modules/bar":{"version":"2.0.0","resolved":"https://example.com/bar-2.0.0.tgz"},
 "node_modules/bar":{"version":"2.1.0","resolved":"https://registry.npmjs.org/bar/-/bar-2.1.0.tgz"},
 "node_modules/qux":{"version":"3.0.0","resolved":"http://example.com/qux-3.0.0.tgz"},
 "node_modules/opt-url":{"version":"4.0.0","resolved":"https://example.com/opt-url-4.0.0.tgz","optional":true},
 "node_modules/follow-redirects":{"version":"1.15.9","resolved":"https://npm.corp.example/follow-redirects/-/follow-redirects-1.15.9.tgz"}
}}`
	dir := tempTree(t, map[string]string{
		"package.json":      `{"name":"app"}`,
		"package-lock.json": lock,
	})
	assertPublicness(t, dir, map[string]string{
		"foo@1.2.3":               priv, // the project's own URL request
		"dev-url@0.1.0":           priv, // devDependency, spaced and upper-case
		"bar@2.0.0":               priv, // axios's nested copy, asked for by URL
		"qux@3.0.0":               priv, // axios's URL request, hoisted to the root
		"opt-url@4.0.0":           priv, // optionalDependency by URL
		"axios@1.12.0":            pub,  // registry.npmjs.org
		"bar@2.1.0":               pub,  // the root's registry copy beside axios's own
		"follow-redirects@1.15.9": pub,  // a company mirror is still a registry
	})
}

func TestPackageLockURLSpecWorkspaceMember(t *testing.T) {
	for _, tc := range []struct {
		name, install string
	}{
		{"own copy", "apps/web/node_modules/foo"},
		{"hoisted", "node_modules/foo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lock := `{"name":"mono","lockfileVersion":3,"packages":{
 "":{"name":"mono","workspaces":["apps/*"]},
 "apps/web":{"name":"web","dependencies":{"foo":"https://example.com/foo-1.2.3.tgz","axios":"^1.12.0"}},
 "apps/admin":{"name":"admin","dependencies":{"lodash":"^4.17.21"}},
 "node_modules/web":{"resolved":"apps/web","link":true},
 "node_modules/admin":{"resolved":"apps/admin","link":true},
 "` + tc.install + `":{"version":"1.2.3","resolved":"https://example.com/foo-1.2.3.tgz"},
 "node_modules/axios":{"version":"1.12.0","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.0.tgz"},
 "node_modules/lodash":{"version":"4.17.21","resolved":"https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz"}
}}`
			root := tempTree(t, map[string]string{
				"package.json":            `{"name":"mono","private":true,"workspaces":["apps/*"]}`,
				"apps/web/package.json":   `{"name":"web","dependencies":{"foo":"https://example.com/foo-1.2.3.tgz","axios":"^1.12.0"}}`,
				"apps/admin/package.json": `{"name":"admin","dependencies":{"lodash":"^4.17.21"}}`,
				"package-lock.json":       lock,
			})
			want := map[string]string{
				"foo@1.2.3": priv, "axios@1.12.0": pub, "lodash@4.17.21": pub,
				"web@": priv, "admin@": priv, // the members' own links
			}
			for _, dir := range []string{
				filepath.Join(root, "apps", "web"),   // the member that asked for it
				filepath.Join(root, "apps", "admin"), // a sibling sharing the lockfile
				root,                                 // the workspace root itself
			} {
				t.Run(filepath.Base(dir), func(t *testing.T) {
					assertPublicness(t, dir, want)
				})
			}
		})
	}
}

// Yarn classic keys each entry by the ranges that asked for it, so a URL
// request shows up in the entry header whoever made it.
func TestYarnLockURLSpecIsPrivate(t *testing.T) {
	const lock = `# THIS IS AN AUTOGENERATED FILE. DO NOT EDIT THIS FILE DIRECTLY.
# yarn lockfile v1


axios@^1.12.0:
  version "1.12.0"
  resolved "https://registry.yarnpkg.com/axios/-/axios-1.12.0.tgz#abc"
  dependencies:
    bar "https://example.com/bar-2.0.0.tgz"
    lodash "^4.17.21"

"bar@https://example.com/bar-2.0.0.tgz":
  version "2.0.0"
  resolved "https://example.com/bar-2.0.0.tgz"

"foo@https://example.com/foo-1.2.3.tgz":
  version "1.2.3"
  resolved "https://example.com/foo-1.2.3.tgz#def"

lodash@^4.17.21:
  version "4.17.21"
  resolved "https://npm.corp.example/lodash/-/lodash-4.17.21.tgz#123"
`
	dir := tempTree(t, map[string]string{
		"package.json": `{"name":"app","dependencies":{"foo":"https://example.com/foo-1.2.3.tgz","axios":"^1.12.0"}}`,
		"yarn.lock":    lock,
	})
	assertPublicness(t, dir, map[string]string{
		"foo@1.2.3":      priv, // the project's own URL request
		"bar@2.0.0":      priv, // axios's URL request
		"axios@1.12.0":   pub,  // registry.yarnpkg.com
		"lodash@4.17.21": pub,  // a company mirror is still a registry
	})

	// The project's specifier alone is enough: a stale or hand-edited header
	// that no longer carries the URL still cannot make the request public.
	deps, err := parseYarnLock([]byte(`foo@1.2.3:
  version "1.2.3"
  resolved "https://example.com/foo-1.2.3.tgz"
`), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || !deps[0].Direct || !deps[0].Private {
		t.Fatalf("direct URL specifier: %+v, want one direct private foo", deps)
	}
}

// pnpm and Bun keep the URL itself where the version goes, and a URL is not a
// release the registry check will ever ask about. Pin that, so a parser that
// starts reading the tarball's version field cannot quietly make these
// packages uploadable.
func TestPnpmAndBunURLSpecVersionIsNotConcrete(t *testing.T) {
	const url = "https://example.com/foo-1.2.3.tgz"
	for _, tc := range []struct {
		file, body string
	}{
		{"pnpm-lock.yaml", `lockfileVersion: '9.0'

importers:

  .:
    dependencies:
      axios:
        specifier: ^1.12.0
        version: 1.12.0
      foo:
        specifier: ` + url + `
        version: ` + url + `

packages:

  axios@1.12.0:
    resolution: {integrity: sha512-test}

  'foo@` + url + `':
    resolution: {tarball: ` + url + `}
    version: 1.2.3
`},
		{"bun.lock", `{
  "lockfileVersion": 1,
  "workspaces": {"": {"dependencies": {"axios": "^1.12.0", "foo": "` + url + `"}}},
  "packages": {
    "axios": ["axios@1.12.0", "", {}, "sha512-test"],
    "foo": ["foo@` + url + `", {}],
  },
}`},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := tempTree(t, map[string]string{tc.file: tc.body})
			pkgs, err := (Adapter{}).ScanPackages(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			foo := mustFindPkg(t, pkgs, "foo")
			if domain.ConcreteResolvedVersion(foo.PURL.Version) {
				t.Fatalf("foo version %q is concrete; the URL copy could be graded PUBLIC", foo.PURL.Version)
			}
			assertPublicness(t, dir, map[string]string{
				"foo@" + foo.PURL.Version: unk,
				"axios@1.12.0":            pub,
			})
		})
	}
}
