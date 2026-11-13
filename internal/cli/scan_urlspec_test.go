package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// A package installed from a tarball URL is recorded under the tarball's own
// name and version, the same shape as the registry release of that name. The
// copy may be a patched fork, so evidence about it must stay on this machine
// even when the registry serves a release with the same coordinate.
func TestScanURLSpecPackagesNeverUpload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		public  []string            // seeded PUBLIC and expected to upload
		private []string            // seeded PUBLIC and expected to stay local
		tree    map[string][]string // uploaded DependsOn, when the lockfile records it
	}{
		{
			name: "package-lock",
			files: map[string]string{
				"package.json": `{"name":"app","dependencies":{"foo":"https://example.com/foo-1.2.3.tgz","axios":"^1.12.0"}}`,
				"package-lock.json": `{"name":"app","lockfileVersion":3,"packages":{
 "":{"name":"app","dependencies":{"foo":"https://example.com/foo-1.2.3.tgz","axios":"^1.12.0"}},
 "node_modules/foo":{"version":"1.2.3","resolved":"https://example.com/foo-1.2.3.tgz"},
 "node_modules/axios":{"version":"1.12.0","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.0.tgz",
   "dependencies":{"bar":"https://example.com/bar-2.0.0.tgz","follow-redirects":"^1.15.6"}},
 "node_modules/bar":{"version":"2.0.0","resolved":"https://example.com/bar-2.0.0.tgz"},
 "node_modules/follow-redirects":{"version":"1.15.9","resolved":"https://registry.npmjs.org/follow-redirects/-/follow-redirects-1.15.9.tgz"}
}}`,
			},
			public:  []string{"axios@1.12.0", "follow-redirects@1.15.9"},
			private: []string{"foo@1.2.3", "bar@2.0.0"},
			tree: map[string][]string{
				"pkg:npm/axios@1.12.0":            {"pkg:npm/follow-redirects@1.15.9"},
				"pkg:npm/follow-redirects@1.15.9": nil,
			},
		},
		{
			name: "yarn",
			files: map[string]string{
				"package.json": `{"name":"app","dependencies":{"foo":"https://example.com/foo-1.2.3.tgz","axios":"^1.12.0"}}`,
				"yarn.lock": `# yarn lockfile v1


axios@^1.12.0:
  version "1.12.0"
  resolved "https://registry.yarnpkg.com/axios/-/axios-1.12.0.tgz#abc"
  dependencies:
    bar "https://example.com/bar-2.0.0.tgz"

"bar@https://example.com/bar-2.0.0.tgz":
  version "2.0.0"
  resolved "https://example.com/bar-2.0.0.tgz"

"foo@https://example.com/foo-1.2.3.tgz":
  version "1.2.3"
  resolved "https://example.com/foo-1.2.3.tgz#def"
`,
			},
			public:  []string{"axios@1.12.0"},
			private: []string{"foo@1.2.3", "bar@2.0.0"},
		},
		{
			name: "yarn-with-transitive-registry-sibling",
			files: map[string]string{
				"package.json": `{"name":"app","dependencies":{"foo":"https://example.com/foo-1.2.3.tgz","axios":"^1.12.0"}}`,
				"yarn.lock": `# yarn lockfile v1


axios@^1.12.0:
  version "1.12.0"
  resolved "https://registry.yarnpkg.com/axios/-/axios-1.12.0.tgz#abc"
  dependencies:
    bar "https://example.com/bar-2.0.0.tgz"
    foo "^2.0.0"

"bar@https://example.com/bar-2.0.0.tgz":
  version "2.0.0"
  resolved "https://example.com/bar-2.0.0.tgz"

"foo@https://example.com/foo-1.2.3.tgz":
  version "1.2.3"
  resolved "https://example.com/foo-1.2.3.tgz#def"

foo@^2.0.0:
  version "2.0.0"
  resolved "https://registry.yarnpkg.com/foo/-/foo-2.0.0.tgz#xyz"
`,
			},
			public:  []string{"axios@1.12.0", "foo@2.0.0"},
			private: []string{"foo@1.2.3", "bar@2.0.0"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runScanURLSpec(t, tc.files, tc.public, tc.private, tc.tree)
		})
	}
}

func runScanURLSpec(t *testing.T, files map[string]string, public, private []string, tree map[string][]string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	var mu sync.Mutex
	var uploaded []domain.ObservationBatch
	var srvErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/evidence/batches" {
			mu.Lock()
			srvErr = fmt.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			mu.Unlock()
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Batches []domain.ObservationBatch `json:"batches"`
		}
		err := json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		if err != nil && srvErr == nil {
			srvErr = err
		} else {
			uploaded = append(uploaded, payload.Batches...)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.DaemonPort = 0
	cfg.ServerURL = srv.URL
	if err := cfg.Save(home); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// Every package is seeded PUBLIC, the URL ones included: that is what the
	// registry answers for a fork that kept the upstream name and version, so
	// only the adapter's PRIVATE marking can keep them from leaving.
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	purlOf := func(nv string) domain.PURL {
		name, version, _ := strings.Cut(nv, "@")
		return domain.PURL{Ecosystem: "npm", Name: name, Version: version}
	}
	for _, nv := range append(append([]string(nil), public...), private...) {
		if err := db.SetPublicness(context.Background(), purlOf(nv), "PUBLIC"); err != nil {
			db.Close()
			t.Fatalf("seed publicness %s: %v", nv, err)
		}
	}
	db.Close()

	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	scanOut, scanCode := captureStdout(t, func() int {
		return Main([]string{"scan", dir})
	})
	if scanCode != 0 {
		t.Fatalf("csx scan returned %d:\n%s", scanCode, scanOut)
	}
	syncOut, syncCode := captureStdout(t, func() int {
		return Main([]string{"sync", "--uploads-only"})
	})
	if syncCode != 0 {
		t.Fatalf("csx sync --uploads-only returned %d:\n%s", syncCode, syncOut)
	}

	mu.Lock()
	gotBatches := append([]domain.ObservationBatch(nil), uploaded...)
	gotErr := srvErr
	mu.Unlock()
	if gotErr != nil {
		t.Fatalf("receiver server error: %v", gotErr)
	}

	var got []string
	for _, b := range gotBatches {
		got = append(got, b.Package)
		for _, nv := range private {
			key := purlOf(nv).String()
			if b.Package == key {
				t.Errorf("uploaded a package installed from a URL: %s", key)
			}
			for _, dep := range b.DependsOn {
				if dep == key {
					t.Errorf("%s: uploaded a dependency installed from a URL: %s", b.Package, dep)
				}
			}
		}
		if tree != nil {
			want, ok := tree[b.Package]
			if !ok || strings.Join(b.DependsOn, ",") != strings.Join(want, ",") {
				t.Errorf("%s: uploaded DependsOn=%v, want %v", b.Package, b.DependsOn, want)
			}
		}
	}
	var want []string
	for _, nv := range public {
		want = append(want, purlOf(nv).String())
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("uploaded packages = %v, want %v", got, want)
	}
}
