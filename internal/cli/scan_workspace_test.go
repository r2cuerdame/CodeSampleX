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
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// A monorepo keeps its only lockfile at the root while commands run inside
// each member. Scanning the member folder must record the root lockfile's
// packages with the member's own direct set, and never upload the sibling
// workspace or the local path it links.
func TestScanWorkspaceMemberEvidence(t *testing.T) {
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
		{"@repo/ui", "0.0.0", true, true},
		{"local-lib", "1.0.0", true, true},
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
		"package.json":             `{"name":"mono","private":true,"workspaces":["apps/*","packages/*"]}`,
		"apps/web/package.json":    `{"name":"web","dependencies":{"axios":"^1.12.0","@repo/ui":"*","local-lib":"file:../../vendor/local-lib"}}`,
		"apps/admin/package.json":  `{"name":"admin","dependencies":{"lodash":"^4.17.21","axios":"^0.27.0"}}`,
		"packages/ui/package.json": `{"name":"@repo/ui","version":"0.0.0"}`,
		"apps/web/src/index.js":    "import axios from 'axios';\nexport const post = axios.post;\n",
		"package-lock.json": `{"name":"mono","lockfileVersion":3,"packages":{
 "":{"name":"mono","workspaces":["apps/*","packages/*"]},
 "apps/web":{"name":"web","dependencies":{"axios":"^1.12.0","@repo/ui":"*","local-lib":"file:../../vendor/local-lib"}},
 "apps/admin":{"name":"admin","dependencies":{"lodash":"^4.17.21","axios":"^0.27.0"}},
 "packages/ui":{"name":"@repo/ui","version":"0.0.0"},
 "vendor/local-lib":{"name":"local-lib","version":"1.0.0"},
 "node_modules/web":{"resolved":"apps/web","link":true},
 "node_modules/admin":{"resolved":"apps/admin","link":true},
 "node_modules/@repo/ui":{"resolved":"packages/ui","link":true},
 "apps/web/node_modules/local-lib":{"resolved":"vendor/local-lib","link":true},
 "node_modules/lodash":{"version":"4.17.21"},
 "node_modules/axios":{"version":"0.27.2"},
 "apps/web/node_modules/axios":{"version":"1.12.0"}
}}`,
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
}
