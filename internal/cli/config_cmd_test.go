package cli

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

func TestConfigSetAndGet(t *testing.T) {
	home := newCLIHome(t, nil)

	out, code := captureStdout(t, func() int {
		return Main([]string{"config", "set", "daemonPort", "50123"})
	})
	if code != 0 {
		t.Fatalf("config set exit = %d\n%s", code, out)
	}
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DaemonPort != 50123 {
		t.Errorf("daemonPort = %d, want 50123", cfg.DaemonPort)
	}

	out, code = captureStdout(t, func() int {
		return Main([]string{"config", "get", "daemonPort"})
	})
	if code != 0 || strings.TrimSpace(out) != "50123" {
		t.Errorf("config get = %q (exit %d), want 50123", strings.TrimSpace(out), code)
	}

	// String value.
	if _, code := captureStdout(t, func() int {
		return Main([]string{"config", "set", "mode", "community"})
	}); code != 0 {
		t.Fatalf("config set mode exit = %d", code)
	}
	cfg, _ = config.Load(home)
	if cfg.Mode != config.ModeCommunity {
		t.Errorf("mode = %q", cfg.Mode)
	}

	// Other fields survive a set (defaults + file merge).
	if cfg.ServerURL != unreachableServer {
		t.Errorf("serverUrl clobbered: %q", cfg.ServerURL)
	}
}

func TestConfigRejectsUnknownKeyAndBadValue(t *testing.T) {
	newCLIHome(t, nil)

	if code := Main([]string{"config", "get", "noSuchKey"}); code != 2 {
		t.Errorf("get unknown key exit = %d, want 2", code)
	}
	if code := Main([]string{"config", "set", "noSuchKey", "1"}); code != 2 {
		t.Errorf("set unknown key exit = %d, want 2", code)
	}
	// Type mismatch: daemonPort must be a number.
	if code := Main([]string{"config", "set", "daemonPort", "not-a-number"}); code != 2 {
		t.Errorf("set bad value exit = %d, want 2", code)
	}
	if code := Main([]string{"config"}); code != 2 {
		t.Errorf("bare config exit = %d, want 2", code)
	}
}
