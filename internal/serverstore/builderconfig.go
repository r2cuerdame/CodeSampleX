package serverstore

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// BuilderConfig collects cmd/csx-builder's environment settings (CSX-451).
//
// It is deliberately its own struct and its own env-var namespace
// (CSX_BUILDER_*), not a field reused from ServerConfig: the whole point of
// the standalone Builder is that its PostgreSQL connection pool is a
// physically separate *pgx.Conn pool from csx-server's -- a second OS
// process, not a second QueryClass sharing csx-server's connPool. Reading
// PoolPolicyFromEnv (CSX_DB_*) here would have let an operator believe they
// were sizing the Builder's pool while actually resizing the server's.
type BuilderConfig struct {
	// DSN is CSX_BUILDER_DSN, falling back to CSX_DSN so a deployment that
	// has not set the new variable still starts: same database, a second
	// connection, exactly like csx-server's own pool already is a second
	// connection from a developer's psql session.
	DSN string
	// Listen is CSX_BUILDER_LISTEN — health/ready/progress only, never the
	// public API. Default ":8091", one above csx-server's default :8080/:8090
	// range so both can run on the same host without a collision.
	Listen string
	// SnapshotInterval and SnapshotPassTimeout are CSX_SNAPSHOT_INTERVAL and
	// CSX_SNAPSHOT_PASS_TIMEOUT — the same two variables csx-server already
	// reads for the in-process Builder, so moving from CSX_BUILDER_MODE=
	// inprocess to standalone does not also require renaming the knobs an
	// operator already has in their compose .env.
	SnapshotInterval    time.Duration
	SnapshotPassTimeout time.Duration
	// DBPool is this process's own admission policy — CSX_BUILDER_DB_*, see
	// BuilderPoolPolicyFromEnv. It is never derived from or compared against
	// csx-server's CSX_DB_* policy.
	DBPool PoolPolicy
	// Lease names the leader lock (internal/compatibility.Leader) so at most
	// one Builder process runs the aggregation pipeline at a time.
	LeaseName       string
	LeaseOwner      string
	LeaseTTL        time.Duration
	LeaseRenewEvery time.Duration
	LeaseRetryEvery time.Duration
}

// defaultBuilderMaxConns is intentionally smaller than csx-server's own
// defaultMaxConns=8. The standalone Builder does exactly one kind of work
// (ClassBackground, unbounded statement timeout by design — see
// PassTimeout) against a single 2GB host already running csx-server and
// PostgreSQL; it does not need — and on this hardware must not claim — a
// pool the size of the interactive-serving process's.
const defaultBuilderMaxConns = 3

// DefaultBuilderPoolPolicy is the shipped, conservative setting for the
// standalone Builder's own pool. ProbeReserve exists so the Builder's own
// /healthz can answer even while its background work has the rest of its
// (small) pool busy — the same shape csx-server's pool already uses, sized
// down to what one background-only process needs.
func DefaultBuilderPoolPolicy() PoolPolicy {
	return PoolPolicy{
		Enabled:          true,
		MaxConns:         defaultBuilderMaxConns,
		ProbeReserve:     1,
		InteractiveConns: 1, // unused by the Builder today; kept valid for normalize()
		BackgroundConns:  2,
		ReadTimeout:      8 * time.Second,
		ReadWait:         3 * time.Second,
		ProbeTimeout:     2 * time.Second,
		ProbeWait:        time.Second,
	}
}

// BuilderConfigFromEnv reads CSX_BUILDER_* (and the CSX_SNAPSHOT_* variables
// shared with the in-process path) with safe defaults.
func BuilderConfigFromEnv() BuilderConfig {
	cfg := BuilderConfig{
		DSN:                 firstNonEmptyEnv("CSX_BUILDER_DSN", "CSX_DSN"),
		Listen:              envOr("CSX_BUILDER_LISTEN", ":8091"),
		SnapshotInterval:    5 * time.Minute,
		SnapshotPassTimeout: defaultSnapshotPassTimeout,
		LeaseName:           envOr("CSX_BUILDER_LEASE_NAME", "compatibility-builder"),
		LeaseOwner:          envOr("CSX_BUILDER_LEASE_OWNER", defaultLeaseOwner()),
		LeaseTTL:            45 * time.Second,
		LeaseRenewEvery:     15 * time.Second,
		LeaseRetryEvery:     10 * time.Second,
	}
	if v := os.Getenv("CSX_SNAPSHOT_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.SnapshotInterval = d
		}
	}
	if v := os.Getenv("CSX_SNAPSHOT_PASS_TIMEOUT"); v != "" {
		if v == "0" {
			cfg.SnapshotPassTimeout = 0
		} else if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			cfg.SnapshotPassTimeout = d
		}
	}
	for _, f := range []struct {
		key string
		dst *time.Duration
	}{
		{"CSX_BUILDER_LEASE_TTL", &cfg.LeaseTTL},
		{"CSX_BUILDER_LEASE_RENEW", &cfg.LeaseRenewEvery},
		{"CSX_BUILDER_LEASE_RETRY", &cfg.LeaseRetryEvery},
	} {
		if v := os.Getenv(f.key); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				*f.dst = d
			}
		}
	}
	cfg.DBPool = BuilderPoolPolicyFromEnv(os.Getenv)
	return cfg
}

// BuilderPoolPolicyFromEnv is PoolPolicyFromEnv's counterpart for the
// standalone Builder's own pool, reading CSX_BUILDER_DB_* instead of
// CSX_DB_*:
//
//	CSX_BUILDER_DB_POOL_GUARD "off" removes the ceiling on the Builder's own pool
//	CSX_BUILDER_DB_MAX_CONNS  total connections this process may hold      (default 3)
//	CSX_BUILDER_DB_PROBE_RESERVE connections only the Builder's own /healthz may take (default 1)
//	CSX_BUILDER_DB_READ_TIMEOUT  statement_timeout, only reachable if something classifies interactive (default 8s)
//	CSX_BUILDER_DB_PROBE_TIMEOUT statement_timeout for the Builder's /healthz (default 2s)
//
// An unparsable value leaves the shipped default in place, for the same
// reason PoolPolicyFromEnv does: a typo in a Builder-only timeout must not
// stop the Builder from starting.
func BuilderPoolPolicyFromEnv(get func(string) string) PoolPolicy {
	pol := DefaultBuilderPoolPolicy()
	if strings.EqualFold(strings.TrimSpace(get("CSX_BUILDER_DB_POOL_GUARD")), "off") {
		pol.Enabled = false
	}
	ints := []struct {
		key string
		dst *int
	}{
		{"CSX_BUILDER_DB_MAX_CONNS", &pol.MaxConns},
		{"CSX_BUILDER_DB_PROBE_RESERVE", &pol.ProbeReserve},
		{"CSX_BUILDER_DB_BACKGROUND_CONNS", &pol.BackgroundConns},
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
		{"CSX_BUILDER_DB_READ_TIMEOUT", &pol.ReadTimeout},
		{"CSX_BUILDER_DB_PROBE_TIMEOUT", &pol.ProbeTimeout},
	}
	for _, f := range durations {
		v := get(f.key)
		if v == "" {
			continue
		}
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

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func defaultLeaseOwner() string { return NewProcessLeaseOwner("csx-builder") }

// NewProcessLeaseOwner identifies one process instance well enough to tell
// it apart from another builder-lease contender started a moment later on
// the same host: a restart must get a new owner name, or a stale renew from
// the process that just died could be mistaken for a live one holding the
// same identity.
//
// cmd/csx-builder uses it for its own owner identity (prefix "csx-builder");
// cmd/csx-server uses it too, for the in-process Builder's owner (prefix
// "csx-server-inprocess") -- the in-process Builder contends for the exact
// same named lease as the standalone one, so that simply having a `builder`
// compose service defined and running can never cause both to materialize
// shards at once, independent of which topology CSX_BUILDER_MODE currently
// selects.
func NewProcessLeaseOwner(prefix string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = prefix
	}
	return fmt.Sprintf("%s:%s:%d:%d", prefix, host, os.Getpid(), time.Now().UnixNano())
}
