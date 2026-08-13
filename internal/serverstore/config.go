package serverstore

import (
	"os"
	"time"
)

// ServerConfig collects every csx-server environment setting (plan contract
// C9). It lives here so cmd/csx-server and the Wave C HTTP layer share one
// definition.
type ServerConfig struct {
	DSN                string        // CSX_DSN — PostgreSQL connection string, required to run
	Listen             string        // CSX_LISTEN — default ":8080"
	BlobDir            string        // CSX_BLOB_DIR — sample artifact blob directory
	PublicURL          string        // CSX_PUBLIC_URL — canonical external base URL
	PublicCheck        string        // CSX_PUBLIC_CHECK — "strict" (default) | "trust" (dev/e2e only)
	SnapshotInterval   time.Duration // CSX_SNAPSHOT_INTERVAL — default 5m
	GithubClientID     string        // CSX_GITHUB_CLIENT_ID — empty ⇒ device flow returns 501
	GithubClientSecret string        // CSX_GITHUB_CLIENT_SECRET
}

// ConfigFromEnv reads the CSX_* server environment with safe defaults.
// DSN deliberately has no default: pointing at a database must be explicit.
func ConfigFromEnv() ServerConfig {
	cfg := ServerConfig{
		DSN:                os.Getenv("CSX_DSN"),
		Listen:             envOr("CSX_LISTEN", ":8080"),
		BlobDir:            envOr("CSX_BLOB_DIR", "blobs"),
		PublicURL:          envOr("CSX_PUBLIC_URL", "http://localhost:8080"),
		PublicCheck:        envOr("CSX_PUBLIC_CHECK", "strict"),
		SnapshotInterval:   5 * time.Minute,
		GithubClientID:     os.Getenv("CSX_GITHUB_CLIENT_ID"),
		GithubClientSecret: os.Getenv("CSX_GITHUB_CLIENT_SECRET"),
	}
	if v := os.Getenv("CSX_SNAPSHOT_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.SnapshotInterval = d
		}
	}
	return cfg
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
