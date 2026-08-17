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
