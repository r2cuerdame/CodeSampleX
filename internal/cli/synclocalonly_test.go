package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

// `csx sync` in a local-only home drove another home's community daemon.
//
// Found by running the shipped v0.1.43 binary against a fresh, isolated
// CSX_HOME in local-only mode. It printed:
//
//	warmed shard keys:  807
//	set aside:          119 (the server rejected these; they are kept, not sent)
//
// from a home that had never synced anything and had no adoption reports at
// all. Those were another home's numbers.
//
// The cause is a gap between two layers that each looked correct. The mode
// gate lives inside Daemon.SyncNow, which is a complete no-op outside
// community mode — internal/daemon/localonly_test.go proves that, and it is
// still true. But syncMain prefers the daemon path, and that path never asked
// what mode the INVOKING home is in; it asked whether some daemon answered.
// And daemon.BaseURLFor, for a home with no daemon.addr file, falls back to
// "127.0.0.1:" + the configured DaemonPort — which is the same default port
// for every home on the machine. So a local-only install with no daemon of
// its own reached a community daemon belonging to a different home and told
// it to sync.
//
// The user-visible result is that "Local-only mode never sends anything" — the
// README's words, and the whole basis on which someone picks that mode — was
// false whenever a second csx home was running on the same machine.
//
// The fix is to gate on the invoking home's own config before anything else:
// a non-community home does not sync, in-process or by delegation, and does
// not go looking for a daemon to ask.

// fakeDaemon stands in for the other home's daemon: it answers the two
// endpoints syncViaDaemon probes and counts what it was asked.
func fakeDaemon(t *testing.T) (port int, calls *atomic.Int64) {
	t.Helper()
	calls = &atomic.Int64{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /local/v1/status", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "community"})
	})
	mux.HandleFunc("POST /local/v1/sync", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// The numbers the shipped binary actually reported.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"warmedKeys": 807, "uploadedBatches": 0,
			"uploadedReports": 0, "setAsideReports": 119,
		})
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: mux}}
	srv.Start()
	t.Cleanup(srv.Close)

	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return port, calls
}

// syncTestHome writes a home whose DaemonPort points at the fake daemon and
// whose ServerURL points nowhere reachable — so if anything does try to sync
// in-process, it cannot quietly succeed against the real network.
func syncTestHome(t *testing.T, mode string, daemonPort int) string {
	t.Helper()
	home := t.TempDir()
	if err := config.EnsureHome(home); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = mode
	cfg.DaemonPort = daemonPort
	cfg.ServerURL = "http://127.0.0.1:1"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	// No daemon.addr: this home has never run a daemon, which is the
	// condition that made BaseURLFor guess the shared default port.
	if _, err := os.Stat(filepath.Join(home, "daemon.addr")); !os.IsNotExist(err) {
		t.Fatalf("test home unexpectedly has a daemon.addr: %v", err)
	}
	t.Setenv("CSX_HOME", home)
	return home
}

func TestSyncInALocalOnlyHomeNeverReachesADaemon(t *testing.T) {
	for _, mode := range []string{config.ModeLocalOnly, config.ModeUninitialized} {
		t.Run(mode, func(t *testing.T) {
			port, calls := fakeDaemon(t)
			syncTestHome(t, mode, port)

			if code := syncMain(context.Background(), nil); code != 0 {
				t.Fatalf("csx sync exited %d; a no-op is not a failure", code)
			}
			if got := calls.Load(); got != 0 {
				t.Errorf("a %s home made %d calls to a daemon on the shared default port; "+
					"local-only means this process transmits nothing at all", mode, got)
			}
		})
	}
}

// The other half: community mode still delegates, or the fix would have
// turned the contract into "sync never works".
func TestSyncInACommunityHomeStillUsesItsDaemon(t *testing.T) {
	port, calls := fakeDaemon(t)
	syncTestHome(t, config.ModeCommunity, port)

	if code := syncMain(context.Background(), nil); code != 0 {
		t.Fatalf("csx sync exited %d", code)
	}
	if got := calls.Load(); got == 0 {
		t.Error("community sync stopped consulting the daemon")
	}
}

func TestSyncRejectsUnknownOptions(t *testing.T) {
	if code := syncMain(context.Background(), []string{"--not-a-sync-option"}); code != 2 {
		t.Fatalf("unknown sync option exited %d, want usage error 2", code)
	}
}

// A local-only sync must also say what it did, or a user who ran it and saw
// zeroes has no way to tell a working no-op from a broken install. The
// llms-install.md step describes exactly this output.
func TestLocalOnlySyncSaysWhyItDidNothing(t *testing.T) {
	port, _ := fakeDaemon(t)
	syncTestHome(t, config.ModeLocalOnly, port)

	out, code := captureStdout(t, func() int { return syncMain(context.Background(), nil) })
	if code != 0 {
		t.Fatalf("csx sync exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "warmed shard keys:") {
		t.Errorf("output does not report the counters at all:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "local-only") {
		t.Errorf("output never says local-only is why nothing happened:\n%s", out)
	}
	if strings.Contains(out, "807") {
		t.Errorf("output carries another home's numbers:\n%s", out)
	}
}
