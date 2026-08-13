package cli

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/search"
)

// unreachableServer fails instantly with no real network traffic.
const unreachableServer = "http://127.0.0.1:1"

// newCLIHome writes a test home (ephemeral daemon port, unreachable
// server) and points CSX_HOME at it.
func newCLIHome(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	cfg.DaemonPort = 0
	cfg.ServerURL = unreachableServer
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Save(home); err != nil {
		t.Fatalf("save config: %v", err)
	}
	t.Setenv("CSX_HOME", home)
	return home
}

// startCLIDaemon runs an in-process daemon over home; cleanup stops it.
func startCLIDaemon(t *testing.T, home string) *daemon.Daemon {
	t.Helper()
	d, err := daemon.New(home)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-errCh:
		cancel()
		t.Fatalf("daemon exited early: %v", err)
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("daemon not ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
		d.Close()
	})
	return d
}

// seedCLISample makes one sample searchable in the daemon's store.
func seedCLISample(t *testing.T, d *daemon.Daemon, id string) {
	t.Helper()
	m := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW",
			Goal:     "upload multipart form with axios",
			Packages: []string{"pkg:npm/axios@1.12.0"},
			Contract: []string{"asserts documented behavior"},
		},
		Packages:        []string{"pkg:npm/axios@1.12.0"},
		Environment:     domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64"},
		License:         "MIT-0",
		ContractCommand: []string{"node", "test/contract.mjs"},
		VerifierAdapter: "node-typescript@1",
	}
	if err := search.SeedSampleDoc(context.Background(), d.DB, m, id, "LOCAL_PASS"); err != nil {
		t.Fatalf("seed sample: %v", err)
	}
}

// captureStdout runs f with os.Stdout redirected to a pipe and returns
// what it printed along with its exit code.
func captureStdout(t *testing.T, f func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	code := f()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	r.Close()
	return string(out), code
}
