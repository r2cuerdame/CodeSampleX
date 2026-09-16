package serverstore

import (
	"testing"
	"time"
)

func TestConfigFromEnvDefaults(t *testing.T) {
	for _, k := range []string{
		"CSX_DSN", "CSX_LISTEN", "CSX_BLOB_DIR", "CSX_PUBLIC_URL",
		"CSX_PUBLIC_CHECK", "CSX_SNAPSHOT_INTERVAL",
		"CSX_GITHUB_CLIENT_ID", "CSX_GITHUB_CLIENT_SECRET",
		"CSX_ACTIVITY_HASH_KEY",
	} {
		t.Setenv(k, "")
	}
	cfg := ConfigFromEnv()
	if cfg.Listen != ":8080" {
		t.Errorf("Listen = %q, want :8080", cfg.Listen)
	}
	if cfg.PublicCheck != "strict" {
		t.Errorf("PublicCheck = %q, want strict (safe default)", cfg.PublicCheck)
	}
	if cfg.SnapshotInterval != 5*time.Minute {
		t.Errorf("SnapshotInterval = %v, want 5m", cfg.SnapshotInterval)
	}
	if cfg.DSN != "" {
		t.Errorf("DSN = %q, want empty (must be provided explicitly)", cfg.DSN)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("CSX_DSN", "postgres://u:p@localhost:5432/csx")
	t.Setenv("CSX_LISTEN", ":9999")
	t.Setenv("CSX_BLOB_DIR", "/data/blobs")
	t.Setenv("CSX_PUBLIC_URL", "https://codesamplex.dev")
	t.Setenv("CSX_PUBLIC_CHECK", "trust")
	t.Setenv("CSX_SNAPSHOT_INTERVAL", "5s")
	t.Setenv("CSX_GITHUB_CLIENT_ID", "cid")
	t.Setenv("CSX_GITHUB_CLIENT_SECRET", "sec")
	t.Setenv("CSX_ACTIVITY_HASH_KEY", "activity-only-key")

	cfg := ConfigFromEnv()
	if cfg.DSN != "postgres://u:p@localhost:5432/csx" {
		t.Errorf("DSN = %q", cfg.DSN)
	}
	if cfg.Listen != ":9999" || cfg.BlobDir != "/data/blobs" ||
		cfg.PublicURL != "https://codesamplex.dev" || cfg.PublicCheck != "trust" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if cfg.SnapshotInterval != 5*time.Second {
		t.Errorf("SnapshotInterval = %v, want 5s", cfg.SnapshotInterval)
	}
	if cfg.GithubClientID != "cid" || cfg.GithubClientSecret != "sec" {
		t.Errorf("github creds not read: %+v", cfg)
	}
	if cfg.ActivityHashKey != "activity-only-key" {
		t.Errorf("activity key not read")
	}
}

func TestConfigFromEnvBadInterval(t *testing.T) {
	t.Setenv("CSX_SNAPSHOT_INTERVAL", "not-a-duration")
	if cfg := ConfigFromEnv(); cfg.SnapshotInterval != 5*time.Minute {
		t.Errorf("bad interval should keep default 5m, got %v", cfg.SnapshotInterval)
	}
}

func TestConfigSnapshotPassTimeout(t *testing.T) {
	t.Run("default bounds a pass", func(t *testing.T) {
		t.Setenv("CSX_SNAPSHOT_PASS_TIMEOUT", "")
		if got := ConfigFromEnv().SnapshotPassTimeout; got != defaultSnapshotPassTimeout {
			t.Fatalf("SnapshotPassTimeout = %s, want %s", got, defaultSnapshotPassTimeout)
		}
	})
	t.Run("headroom over typical full pass cost", func(t *testing.T) {
		// In #174 a typical full pass required ~100-120 minutes. A ceiling at or
		// under that would truncate healthy work and leave the builder restarting
		// a pass it can never finish.
		const typicalFullPass = 120 * time.Minute
		if defaultSnapshotPassTimeout <= typicalFullPass {
			t.Fatalf("default ceiling %s does not clear the %s full pass cost noted in #174",
				defaultSnapshotPassTimeout, typicalFullPass)
		}
		if defaultSnapshotPassTimeout >= 24*time.Hour {
			t.Fatalf("default ceiling %s reaches the 24h resume window", defaultSnapshotPassTimeout)
		}
	})
	t.Run("operator override", func(t *testing.T) {
		t.Setenv("CSX_SNAPSHOT_PASS_TIMEOUT", "45m")
		if got := ConfigFromEnv().SnapshotPassTimeout; got != 45*time.Minute {
			t.Fatalf("SnapshotPassTimeout = %s, want 45m", got)
		}
	})
	t.Run("bare zero removes the ceiling", func(t *testing.T) {
		t.Setenv("CSX_SNAPSHOT_PASS_TIMEOUT", "0")
		if got := ConfigFromEnv().SnapshotPassTimeout; got != 0 {
			t.Fatalf("SnapshotPassTimeout = %s, want 0 (unbounded)", got)
		}
	})
	t.Run("zero durations remove the ceiling", func(t *testing.T) {
		for _, raw := range []string{"0", "0s", "0m", "0h"} {
			t.Setenv("CSX_SNAPSHOT_PASS_TIMEOUT", raw)
			if got := ConfigFromEnv().SnapshotPassTimeout; got != 0 {
				t.Fatalf("SnapshotPassTimeout for %q = %s, want 0 (unbounded)", raw, got)
			}
		}
	})
	t.Run("garbage keeps the default", func(t *testing.T) {
		t.Setenv("CSX_SNAPSHOT_PASS_TIMEOUT", "not-a-duration")
		if got := ConfigFromEnv().SnapshotPassTimeout; got != defaultSnapshotPassTimeout {
			t.Fatalf("SnapshotPassTimeout = %s, want the default %s", got, defaultSnapshotPassTimeout)
		}
	})
}

// CSX_GOVERNOR_ENABLED is the resource governor's (#454) no-build rollback,
// and docs/operations.md tells an operator to reach for it during an
// incident. It ships on; only "off" turns it off, and the parse is
// case-insensitive and tolerant of surrounding whitespace because what an
// operator actually writes into a compose .env at 3am is not guaranteed to
// be lowercase and trimmed.
func TestConfigGovernorEnabled(t *testing.T) {
	t.Run("ships on", func(t *testing.T) {
		t.Setenv("CSX_GOVERNOR_ENABLED", "")
		if !ConfigFromEnv().GovernorEnabled {
			t.Fatal("GovernorEnabled = false with the variable unset; the governor ships on")
		}
	})
	t.Run("off disables it", func(t *testing.T) {
		for _, raw := range []string{"off", "OFF", "Off", "  off  "} {
			t.Setenv("CSX_GOVERNOR_ENABLED", raw)
			if ConfigFromEnv().GovernorEnabled {
				t.Fatalf("GovernorEnabled = true for %q; the documented rollback did not take", raw)
			}
		}
	})
	// Only "off" is the rollback. A value nobody defined must not be read as
	// "disable the load shedder" -- a typo would then turn the governor off
	// silently, which is the failure this whole knob exists to avoid being
	// ambiguous about.
	t.Run("anything else leaves it on", func(t *testing.T) {
		for _, raw := range []string{"on", "false", "0", "no", "disabled", "of"} {
			t.Setenv("CSX_GOVERNOR_ENABLED", raw)
			if !ConfigFromEnv().GovernorEnabled {
				t.Fatalf("GovernorEnabled = false for %q; only \"off\" is the documented rollback", raw)
			}
		}
	})
}
