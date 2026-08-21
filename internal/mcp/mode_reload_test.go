package mcp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// An MCP server normally lives for the whole editor session. Changing the
// persisted mode must revoke that already-running process; restarting the
// editor cannot be part of the privacy contract.
func TestNewDepsReloadsCommunityRevocationBeforeRemoteWork(t *testing.T) {
	var remoteCalls atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		remoteCalls.Add(1)
		http.NotFound(w, nil)
	}))
	defer remote.Close()

	home := t.TempDir()
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = remote.URL
	if err := cfg.Save(home); err != nil {
		t.Fatalf("save community config: %v", err)
	}

	deps, closeDB, err := NewDeps(home)
	if err != nil {
		t.Fatalf("NewDeps: %v", err)
	}
	defer closeDB() //nolint:errcheck

	first := domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "use left-pad",
		Packages:      []string{"pkg:npm/left-pad@1.3.0"},
		Symbols:       []string{"leftPad"},
	}
	if resp, _ := deps.Search(t.Context(), first); !resp.Miss {
		t.Fatal("empty community cache unexpectedly returned a hit")
	}
	if remoteCalls.Load() == 0 {
		t.Fatal("community search did not perform its on-demand shard fetch")
	}
	pendingBefore := pendingQueueCount(t, home)
	if pendingBefore == 0 {
		t.Fatal("community miss did not queue its privacy-reduced Wanted candidate")
	}
	callsBefore := remoteCalls.Load()

	// Revoke consent without rebuilding deps: this is the real editor/MCP
	// lifecycle that used to retain cfg.Mode == community indefinitely.
	cfg.Mode = config.ModeLocalOnly
	if err := cfg.Save(home); err != nil {
		t.Fatalf("save local-only config: %v", err)
	}
	if got := deps.Mode(); got != config.ModeLocalOnly {
		t.Fatalf("running MCP mode = %q, want reloaded local-only", got)
	}

	second := domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "use is-even",
		Packages:      []string{"pkg:npm/is-even@1.0.0"},
		Symbols:       []string{"isEven"},
	}
	if resp, _ := deps.Search(t.Context(), second); !resp.Miss {
		t.Fatal("empty local-only cache unexpectedly returned a hit")
	}

	// A cache miss in get_sample used to use the startup-mode peer/server
	// fetcher. It may now return a local metadata-only result or this
	// path-free refusal, but it must not touch a remote source.
	missingID := "sha256:" + strings.Repeat("0", 64)
	_, _, fetchErr := deps.GetSample(t.Context(), missingID)
	if fetchErr == nil {
		t.Fatal("uncached local-only sample unexpectedly resolved")
	}
	if strings.Contains(fetchErr.Error(), home) {
		t.Fatalf("mode refusal exposed the CSX home path: %v", fetchErr)
	}

	// Adoption and run evidence are also upload queues. Both remain useful
	// locally, but a post-revocation call may not add an uploadable row.
	pass := true
	correlationDB, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	offerID, err := correlationDB.RecordSearchOffer(t.Context(), localdb.HitRow{SampleID: missingID}, localdb.InterventionRow{
		SampleID: missingID, ExactFailureMatched: true, VerifiedOffer: true,
	})
	if err != nil {
		correlationDB.Close()
		t.Fatal(err)
	}
	if err := correlationDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.ReportAdoption(t.Context(), offerID, missingID, true, &pass); err != nil {
		t.Fatalf("record local adoption: %v", err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "package.json"), []byte(
		`{"name":"mode-reload-test","dependencies":{"axios":"1.12.0"}}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, "package-lock.json"), []byte(
		`{"lockfileVersion":3,"packages":{"node_modules/axios":{"version":"1.12.0"}}}`), 0o600); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
	if code, _, _, _, _, err := deps.RunObserved(t.Context(), []string{"go", "version"}, project); err != nil || code != 0 {
		t.Fatalf("local-only observed command: code=%d err=%v", code, err)
	}

	if got := remoteCalls.Load(); got != callsBefore {
		t.Fatalf("revoked running MCP made %d additional remote call(s)", got-callsBefore)
	}
	if got := pendingQueueCount(t, home); got != pendingBefore {
		t.Fatalf("revoked running MCP added %d upload queue row(s)", got-pendingBefore)
	}
	stats, err := deps.LocalStats(t.Context())
	if err != nil {
		t.Fatalf("LocalStats: %v", err)
	}
	if got := stats["mode"]; got != config.ModeLocalOnly {
		t.Fatalf("running MCP stats mode = %v, want local-only", got)
	}
	if got := stats["pendingObservations"]; got != 0 {
		t.Fatalf("revoked running MCP retained uploadable observations: %v", got)
	}
}

func pendingQueueCount(t *testing.T, home string) int {
	t.Helper()
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("open local DB: %v", err)
	}
	defer db.Close()
	rows, err := db.QueuePending(t.Context(), 100)
	if err != nil {
		t.Fatalf("list upload queue: %v", err)
	}
	return len(rows)
}
