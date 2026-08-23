package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

// Three public documents describe what local-only mode does on the wire, and
// they disagreed with each other and with this package.
//
// The README said "Local-only mode never sends anything", which is what the
// tests above actually hold. llms-install.md said `csx sync` "works in
// local-only mode: warming downloads shards, it uploads nothing" — the
// opposite, and the more dangerous direction, because a reader who chose
// local-only for privacy was told a request goes out that does not.
//
// The contract is the code's: SyncNow is a complete no-op outside community
// mode, popularity-list GET included. This test measures that once more and
// then holds the documents to the measurement, so the next person to edit
// either one has to change the behaviour first.

func repoRootForDocs(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

func TestThePublishedLocalOnlyContractIsTheOneTheCodeKeeps(t *testing.T) {
	// Measure first. Everything asserted about the documents below is only
	// worth asserting because this passes.
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeLocalOnly
		c.ServerURL = srv.URL
		c.PinnedPackages = []string{"pkg:npm/react@19.2.0"}
	})
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	res := d.SyncNow(context.Background())
	if res.WarmedKeys != 0 {
		t.Fatalf("local-only sync warmed %d shard keys", res.WarmedKeys)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("local-only sync made %d server requests", got)
	}

	root := repoRootForDocs(t)

	// llms-install.md is the document an agent is pointed at, and it is the
	// one that had it backwards.
	guide := readDocFile(t, filepath.Join(root, "llms-install.md"))
	for _, wrong := range []string{
		"Works in local-only mode: warming",
		"warming\ndownloads shards",
		"warming downloads shards",
	} {
		if strings.Contains(guide, wrong) {
			t.Errorf("llms-install.md still says %q; local-only sync downloads nothing", wrong)
		}
	}
	if !strings.Contains(guide, "local-only") {
		t.Error("llms-install.md's sync step no longer mentions local-only at all")
	}

	// The README's version was already right, and stays that way.
	readme := readDocFile(t, filepath.Join(root, "README.md"))
	if !strings.Contains(readme, "Local-only mode never sends anything") {
		t.Error("README.md no longer states that local-only mode never sends anything")
	}
}

func readDocFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
