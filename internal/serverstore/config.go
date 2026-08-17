package serverstore

import (
	"os"
	"strconv"
	"time"
)

// ServerConfig collects every csx-server environment setting (plan contract
// C9). It lives here so cmd/csx-server and the Wave C HTTP layer share one
// definition.
type ServerConfig struct {
	DSN         string // CSX_DSN — PostgreSQL connection string, required to run
	Listen      string // CSX_LISTEN — default ":8080"
	BlobDir     string // CSX_BLOB_DIR — sample artifact blob directory
	PublicURL   string // CSX_PUBLIC_URL — canonical external base URL
	PublicCheck string // CSX_PUBLIC_CHECK — "strict" (default) | "trust" (dev/e2e only)
	// Publishing is who may upload a sample: "seeded" (default) requires a
	// token that resolves to a seeder identity, "open" allows anonymous
	// upload and exists for local development and e2e runs only.
	//
	// The shipped policy is seeded. A sample is worth what its provenance is
	// worth, and code from an origin the network cannot establish cannot be
	// given that guarantee -- see internal/httpapi/publishgate.go. Evidence,
	// receipts and every read stay anonymous.
	Publishing         string        // CSX_PUBLISHING — "seeded" (default) | "open" (dev/e2e only)
	SnapshotInterval   time.Duration // CSX_SNAPSHOT_INTERVAL — default 5m
	GithubClientID     string        // CSX_GITHUB_CLIENT_ID — empty ⇒ device flow returns 501
	GithubClientSecret string        // CSX_GITHUB_CLIENT_SECRET
	// AdminTokenSHA256 enables the private read-only /admin route only when
	// it is a valid hexadecimal SHA-256 digest. It is intentionally separate
	// from seeder credentials and never accepts a raw token.
	AdminTokenSHA256 string // CSX_ADMIN_TOKEN_SHA256
	// BlobBudgetBytes caps total artifact storage (CSX_BLOB_BUDGET_MB, 0 =
	// unlimited). Sample upload is anonymous, so this is the only ceiling
	// on how much disk an unauthenticated caller can take — and the volume
	// is shared with PostgreSQL, which stops working when it fills.
	BlobBudgetBytes int64
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
		Publishing:         envOr("CSX_PUBLISHING", "seeded"),
		SnapshotInterval:   5 * time.Minute,
		GithubClientID:     os.Getenv("CSX_GITHUB_CLIENT_ID"),
		GithubClientSecret: os.Getenv("CSX_GITHUB_CLIENT_SECRET"),
		AdminTokenSHA256:   os.Getenv("CSX_ADMIN_TOKEN_SHA256"),
	}
	if v := os.Getenv("CSX_SNAPSHOT_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.SnapshotInterval = d
		}
	}
	// Default 20GB: the production volume is 60GB shared with PostgreSQL,
	// so artifacts get a third and the database keeps room to breathe.
	cfg.BlobBudgetBytes = 20 << 30
	if v := os.Getenv("CSX_BLOB_BUDGET_MB"); v != "" {
		if mb, err := strconv.ParseInt(v, 10, 64); err == nil && mb >= 0 {
			cfg.BlobBudgetBytes = mb << 20
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
