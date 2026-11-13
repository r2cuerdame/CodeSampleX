package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// A monorepo keeps its only lockfile at the root while commands run inside
// each member. Scanning the member folder must record the root lockfile's
// packages with the member's own direct set and the tree beneath them, and
// never upload the sibling workspace or the local paths it links.
func TestScanWorkspaceMemberEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, workspaces string
	}{
		{"simple", `["apps/*","packages/*"]`},
		{"brace", `["{apps,packages}/*"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runScanWorkspaceMemberEvidence(t, tc.workspaces)
		})
	}
}

func runScanWorkspaceMemberEvidence(t *testing.T, workspacesJSON string) {
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

	// Every package is seeded PUBLIC, the private ones included, so only the
	// adapter's PRIVATE marking can keep them from leaving the machine.
	wantDirect := map[string]bool{}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, p := range []struct {
		name, version string
		direct, priv  bool
	}{
		{"axios", "1.12.0", true, false},
		{"axios", "0.27.2", false, false},
		{"lodash", "4.17.21", false, false},
		{"follow-redirects", "1.15.9", false, false},
		{"follow-redirects", "1.15.6", false, false},
		{"proxy-from-env", "1.1.0", false, false},
		{"@repo/ui", "0.0.0", true, true},
		{"local-lib", "1.0.0", true, true},
		{"proxy-from-env", "1.0.0", true, true},
	} {
		purl := domain.PURL{Ecosystem: "npm", Name: p.name, Version: p.version}
		if !p.priv {
			wantDirect[purl.String()] = p.direct
		}
		if err := db.SetPublicness(context.Background(), purl, "PUBLIC"); err != nil {
			db.Close()
			t.Fatalf("seed publicness %s: %v", purl, err)
		}
	}
	db.Close()

	root := t.TempDir()
	for name, body := range map[string]string{
		"package.json":             fmt.Sprintf(`{"name":"mono","private":true,"workspaces":%s}`, workspacesJSON),
		"apps/web/package.json":    `{"name":"web","dependencies":{"axios":"^1.12.0","@repo/ui":"*","local-lib":"file:../../vendor/local-lib","proxy-from-env":"file:../../vendor/proxy-from-env"}}`,
		"apps/admin/package.json":  `{"name":"admin","dependencies":{"lodash":"^4.17.21","axios":"^0.27.0","proxy-from-env":"^1.1.0"}}`,
		"packages/ui/package.json": `{"name":"@repo/ui","version":"0.0.0"}`,
		"apps/web/src/index.js":    "import axios from 'axios';\nexport const post = axios.post;\n",
		// apps/web keeps its own follow-redirects and links a local
		// proxy-from-env; admin's hoisted copies are the root's.
		"package-lock.json": fmt.Sprintf(`{"name":"mono","lockfileVersion":3,"packages":{
 "":{"name":"mono","workspaces":%s},
 "apps/web":{"name":"web","dependencies":{"axios":"^1.12.0","@repo/ui":"*","local-lib":"file:../../vendor/local-lib","proxy-from-env":"file:../../vendor/proxy-from-env"}},
 "apps/admin":{"name":"admin","dependencies":{"lodash":"^4.17.21","axios":"^0.27.0","proxy-from-env":"^1.1.0"}},
 "packages/ui":{"name":"@repo/ui","version":"0.0.0"},
 "vendor/local-lib":{"name":"local-lib","version":"1.0.0"},
 "vendor/proxy-from-env":{"name":"proxy-from-env","version":"1.0.0"},
 "node_modules/web":{"resolved":"apps/web","link":true},
 "node_modules/admin":{"resolved":"apps/admin","link":true},
 "node_modules/@repo/ui":{"resolved":"packages/ui","link":true},
 "apps/web/node_modules/local-lib":{"resolved":"vendor/local-lib","link":true},
 "apps/web/node_modules/proxy-from-env":{"resolved":"vendor/proxy-from-env","link":true},
 "node_modules/lodash":{"version":"4.17.21"},
 "node_modules/proxy-from-env":{"version":"1.1.0"},
 "node_modules/follow-redirects":{"version":"1.15.6"},
 "node_modules/axios":{"version":"0.27.2","dependencies":{"follow-redirects":"^1.14.9"}},
 "apps/web/node_modules/axios":{"version":"1.12.0","dependencies":{"follow-redirects":"^1.15.6","proxy-from-env":"^1.1.0"}},
 "apps/web/node_modules/follow-redirects":{"version":"1.15.9"}
}}`, workspacesJSON),
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	member := filepath.Join(root, "apps", "web")

	pending := func() []localdb.ObsRow {
		t.Helper()
		db, err := localdb.Open(filepath.Join(home, "csx.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rows, err := db.PendingObservations(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}

	dryOut, dryCode := captureStdout(t, func() int {
		return Main([]string{"scan", member, "--dry-run"})
	})
	if dryCode != 0 {
		t.Fatalf("csx scan --dry-run returned %d:\n%s", dryCode, dryOut)
	}
	if !strings.Contains(dryOut, "dry run") {
		t.Errorf("--dry-run output missing marker:\n%s", dryOut)
	}
	if rows := pending(); len(rows) != 0 {
		t.Fatalf("--dry-run recorded %d observations, want 0", len(rows))
	}

	scanOut, scanCode := captureStdout(t, func() int {
		return Main([]string{"scan", member})
	})
	if scanCode != 0 {
		t.Fatalf("csx scan returned %d:\n%s", scanCode, scanOut)
	}
	rows := pending()
	if len(rows) != len(wantDirect) {
		t.Fatalf("pending observations = %d, want %d: %+v", len(rows), len(wantDirect), rows)
	}
	for _, r := range rows {
		want, ok := wantDirect[r.PURL]
		if !ok {
			t.Errorf("unexpected observation package %q: %+v", r.PURL, r)
			continue
		}
		if r.Stage != domain.StageUsed || r.Result != domain.ResultPass {
			t.Errorf("%s: got stage=%q result=%q, want USED PASS", r.PURL, r.Stage, r.Result)
		}
		if r.Direct != want {
			t.Errorf("%s: pending row Direct = %v, want %v", r.PURL, r.Direct, want)
		}
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
	if len(gotBatches) != len(wantDirect) {
		t.Fatalf("uploaded %d batches, want %d: %+v", len(gotBatches), len(wantDirect), gotBatches)
	}
	for _, batch := range gotBatches {
		want, ok := wantDirect[batch.Package]
		if !ok {
			t.Errorf("unexpected uploaded package %q: %+v", batch.Package, batch)
			continue
		}
		if batch.Direct != want {
			t.Errorf("%s: uploaded batch Direct = %v, want %v", batch.Package, batch.Direct, want)
		}
	}

	// The tree comes from the root lockfile too. apps/web's axios resolves its
	// own follow-redirects, and the proxy-from-env it links locally is no
	// edge at all — never the root's public copy it does not load.
	wantTree := map[string]struct {
		dependsOn []string
		none      bool
	}{
		"pkg:npm/axios@1.12.0":            {[]string{"pkg:npm/follow-redirects@1.15.9"}, false},
		"pkg:npm/axios@0.27.2":            {[]string{"pkg:npm/follow-redirects@1.15.6"}, false},
		"pkg:npm/lodash@4.17.21":          {nil, true},
		"pkg:npm/follow-redirects@1.15.9": {nil, true},
		"pkg:npm/follow-redirects@1.15.6": {nil, true},
		"pkg:npm/proxy-from-env@1.1.0":    {nil, true},
	}
	for _, batch := range gotBatches {
		want := wantTree[batch.Package]
		if strings.Join(batch.DependsOn, ",") != strings.Join(want.dependsOn, ",") || batch.DependsOnNone != want.none {
			t.Errorf("%s: uploaded DependsOn=%v DependsOnNone=%v, want %v %v",
				batch.Package, batch.DependsOn, batch.DependsOnNone, want.dependsOn, want.none)
		}
		for _, dep := range batch.DependsOn {
			if strings.Contains(dep, "repo/ui") || strings.Contains(dep, "local-lib") || strings.Contains(dep, "proxy-from-env") {
				t.Errorf("%s: uploaded a dependency on a linked workspace or local path: %s", batch.Package, dep)
			}
		}
	}

	store := serverstore.NewFake()
	if _, rejected, err := store.IngestBatches(context.Background(), gotBatches); err != nil || len(rejected) != 0 {
		t.Fatalf("ingest uploaded batches: rejected=%+v err=%v", rejected, err)
	}
	for _, c := range []struct {
		name, version string
		none          bool
	}{
		{"lodash", "4.17.21", true},
		{"follow-redirects", "1.15.9", true},
		{"axios", "1.12.0", false},
	} {
		got, err := store.DependencyProvenNone(context.Background(), "npm", c.name, c.version)
		if err != nil || got != c.none {
			t.Errorf("DependencyProvenNone(%s@%s) = %v, %v; want %v", c.name, c.version, got, err, c.none)
		}
	}
}
