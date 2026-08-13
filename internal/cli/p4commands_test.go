package cli

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

func TestUINoOpenPrintsURL(t *testing.T) {
	home := newCLIHome(t, nil)
	startCLIDaemon(t, home)

	out, code := captureStdout(t, func() int {
		return Main([]string{"ui", "--no-open"})
	})
	if code != 0 {
		t.Fatalf("ui exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "/ui") || !strings.Contains(out, "http://127.0.0.1:") {
		t.Errorf("ui output = %q, want dashboard URL", out)
	}
}

func TestSyncViaDaemonAndFallback(t *testing.T) {
	home := newCLIHome(t, nil)
	startCLIDaemon(t, home)

	out, code := captureStdout(t, func() int {
		return Main([]string{"sync"})
	})
	if code != 0 {
		t.Fatalf("sync exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "warmed shard keys") {
		t.Errorf("sync output = %q", out)
	}
}

func TestSyncDirectWhenDaemonDown(t *testing.T) {
	// Port 1: nothing listens, probe fails instantly → direct fallback.
	newCLIHome(t, func(cfg *config.Config) { cfg.DaemonPort = 1 })

	out, code := captureStdout(t, func() int {
		return Main([]string{"sync"})
	})
	if code != 0 {
		t.Fatalf("sync exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "uploaded batches") {
		t.Errorf("sync output = %q", out)
	}
}

func TestStatsTableShowsEstimatedLabel(t *testing.T) {
	newCLIHome(t, func(cfg *config.Config) { cfg.DaemonPort = 1 })

	out, code := captureStdout(t, func() int {
		return Main([]string{"stats"})
	})
	if code != 0 {
		t.Fatalf("stats exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "Estimated reasoning avoided") || !strings.Contains(out, "Estimated") {
		t.Errorf("stats output missing Estimated label:\n%s", out)
	}
	if !strings.Contains(out, "Hits / Misses") {
		t.Errorf("stats output missing hit/miss line:\n%s", out)
	}
}

func TestStatsJSONAlwaysFlagsEstimate(t *testing.T) {
	newCLIHome(t, func(cfg *config.Config) { cfg.DaemonPort = 1 })

	out, code := captureStdout(t, func() int {
		return Main([]string{"stats", "--json"})
	})
	if code != 0 {
		t.Fatalf("stats --json exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, `"estimated": true`) {
		t.Errorf("stats JSON must flag the estimate:\n%s", out)
	}
}

func TestDaemonStatusAndStop(t *testing.T) {
	home := newCLIHome(t, nil)
	startCLIDaemon(t, home)

	out, code := captureStdout(t, func() int {
		return Main([]string{"daemon", "status"})
	})
	if code != 0 || !strings.Contains(out, "running") {
		t.Fatalf("daemon status exit=%d output=%q", code, out)
	}

	if code := Main([]string{"daemon", "bogus"}); code != 2 {
		t.Errorf("daemon bogus exit = %d, want 2", code)
	}

	out, code = captureStdout(t, func() int {
		return Main([]string{"daemon", "stop"})
	})
	if code != 0 || !strings.Contains(out, "stopping") {
		t.Errorf("daemon stop exit=%d output=%q", code, out)
	}
}

func TestP4CommandsRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, c := range Commands() {
		names[c.Name] = true
	}
	for _, want := range []string{"daemon", "ui", "sync", "search", "stats", "config"} {
		if !names[want] {
			t.Errorf("command %q not registered", want)
		}
	}
}
