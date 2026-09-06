package serverstore

import (
	"os"
	"strconv"
	"strings"
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
	// AdminTokenSHA256 enables the private /admin operator route only when
	// it is a valid hexadecimal SHA-256 digest. It is intentionally separate
	// from seeder credentials and never accepts a raw token.
	AdminTokenSHA256 string // CSX_ADMIN_TOKEN_SHA256
	// ActivityHashKey is a dedicated 256-bit hex key for period-scoped
	// external network estimates. It must never reuse an admin credential.
	ActivityHashKey string // CSX_ACTIVITY_HASH_KEY
	// BlobBudgetBytes caps total artifact storage (CSX_BLOB_BUDGET_MB, 0 =
	// unlimited). Sample upload is anonymous, so this is the only ceiling
	// on how much disk an unauthenticated caller can take — and the volume
	// is shared with PostgreSQL, which stops working when it fills.
	BlobBudgetBytes int64
	// DBPool is the connection-pool timeout and admission policy
	// (CSX_DB_*). It is configuration and not a constant because the only
	// honest rollback for a defense like this is one an operator can apply
	// without a build — see docs/operations.md.
	DBPool PoolPolicy
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
		ActivityHashKey:    os.Getenv("CSX_ACTIVITY_HASH_KEY"),
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
	cfg.DBPool = PoolPolicyFromEnv(os.Getenv)
	return cfg
}

// PoolPolicyFromEnv reads the database pool settings, starting from the
// shipped policy and changing only what is named:
//
//	CSX_DB_POOL_GUARD    "off" restores the pre-R2C-58 pool entirely
//	CSX_DB_MAX_CONNS     total connections (default 8)
//	CSX_DB_PROBE_RESERVE connections only /healthz may take (default 1)
//	CSX_DB_READ_CONNS    ceiling on user-facing reads (default 6)
//	CSX_DB_WRITE_CONNS   ceiling on ingest and background work (default 4)
//	CSX_DB_READ_TIMEOUT  statement_timeout for reads, 0 = none (default 8s)
//	CSX_DB_READ_WAIT     how long a read queues before 503, 0 = forever (3s)
//	CSX_DB_PROBE_TIMEOUT statement_timeout for /healthz (default 2s)
//
// An unparsable value leaves the shipped default in place. This is the one
// place in this file where that is the right failure: a typo in a timeout
// must not take the server down at boot, and the default it falls back to
// is the value the deployment was already tested with.
func PoolPolicyFromEnv(get func(string) string) PoolPolicy {
	pol := DefaultPoolPolicy()
	if strings.EqualFold(strings.TrimSpace(get("CSX_DB_POOL_GUARD")), "off") {
		pol.Enabled = false
	}
	ints := []struct {
		key string
		dst *int
	}{
		{"CSX_DB_MAX_CONNS", &pol.MaxConns},
		{"CSX_DB_PROBE_RESERVE", &pol.ProbeReserve},
		{"CSX_DB_READ_CONNS", &pol.InteractiveConns},
		{"CSX_DB_WRITE_CONNS", &pol.BackgroundConns},
	}
	for _, f := range ints {
		if v := get(f.key); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				*f.dst = n
			}
		}
	}
	durations := []struct {
		key string
		dst *time.Duration
	}{
		{"CSX_DB_READ_TIMEOUT", &pol.ReadTimeout},
		{"CSX_DB_READ_WAIT", &pol.ReadWait},
		{"CSX_DB_PROBE_TIMEOUT", &pol.ProbeTimeout},
	}
	for _, f := range durations {
		v := get(f.key)
		if v == "" {
			continue
		}
		// "0" means "no ceiling" and has to be reachable without writing
		// "0s", because that is what an operator types at 3am.
		if v == "0" {
			*f.dst = 0
			continue
		}
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			*f.dst = d
		}
	}
	return pol.normalize()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
