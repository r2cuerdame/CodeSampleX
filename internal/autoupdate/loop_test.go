package autoupdate

import (
	"context"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

func TestLoopReloadsRevokedConsentBeforeNetwork(t *testing.T) {
	for _, name := range []string{
		"CSX_LAUNCHER_ROOT", "CSX_LAUNCHER_PATH", "CSX_LAUNCHER_VERSION",
		"CSX_PAYLOAD_VERSION", "CSX_ACTIVE_SEQUENCE", "CSX_ACTIVE_SHA256",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "csx")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := csxupdate.AdoptStandalone(home, exe); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.AutoUpdate = "auto"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	if err := csxupdate.SaveState(home, csxupdate.State{Schema: 1, NextCheck: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	oldInterval, oldCheck := PollInterval, RunCheck
	PollInterval = 50 * time.Millisecond
	var checks atomic.Int32
	RunCheck = func(context.Context, *csxupdate.Client) (csxupdate.Result, error) {
		checks.Add(1)
		return csxupdate.Result{}, nil
	}
	t.Cleanup(func() { PollInterval, RunCheck = oldInterval, oldCheck })

	out := Loop(context.Background(), home, cfg, exe, "v1.0.0")
	time.Sleep(10 * time.Millisecond)
	cfg.AutoUpdate = "off"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("automatic update loop did not stop after consent revocation")
	}
	if got := checks.Load(); got != 0 {
		t.Fatalf("network check ran after consent revocation: %d", got)
	}
}
