// Package serverstore is the csx-server persistence layer: a Store interface
// over PostgreSQL (plan contract C4) plus the pure ingest logic — batch
// validation and delta-merge dedup semantics — which unit-tests without a
// database. This file must stay free of pgx imports; the pgx implementation
// lives in pg.go.
package serverstore

import (
	"context"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// RejectedBatch reports one refused batch of an ingest call, mirroring the
// C5 wire shape {index, reason}.
type RejectedBatch struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// PackageRow is one packages-table row.
type PackageRow struct {
	PURL       string
	Ecosystem  string
	Name       string
	Version    string
	Major      string
	Publicness string    // PUBLIC | PRIVATE | UNKNOWN
	CheckedAt  time.Time // zero = publicness never checked
	FirstSeen  time.Time
	LastSeen   time.Time
}

// SnapshotTarget is one (purl, symbol) pair that has evidence and therefore
// needs a compatibility snapshot. Symbol "" is the package-level row.
type SnapshotTarget struct {
	PURL   string
	Symbol string
}

// EvidenceRow is one aggregated evidence_agg row.
type EvidenceRow struct {
	PURL                 string
	Symbol               string
	SymbolConfidence     string
	EnvHash              string
	EnvJSON              string
	Stage                string
	Result               string
	ErrorFingerprint     string
	ErrorCode            string
	ObservationCount     int64
	UniquePeerBuckets    int
	UniqueProjectBuckets int
	FirstSeen            time.Time
	LastSeen             time.Time
}

// SampleRow is one samples-table row. ManifestJSON is the csx.json document.
type SampleRow struct {
	SampleID     string
	CaseID       string // "" ⇒ NULL (case unknown)
	ManifestJSON string
	Status       string // PUBLISHED | CROSS_PASS | MATRIX_PASS | STABLE
	OriginSeeder string
	License      string
	SizeBytes    int64
	HotScore     float64
	CreatedAt    time.Time
}

// ReceiptRow is one receipts-table row. ReceiptJSON is the full signed
// VerificationReceipt document.
type ReceiptRow struct {
	ReceiptID      string
	SampleID       string
	PeerID         string
	EnvHash        string
	ReceiptJSON    string
	ContractResult string // PASS | FAIL | SKIPPED | ""
	CreatedAt      time.Time
}

// JobRow is one verification_jobs-table row.
type JobRow struct {
	ID          int64
	SampleID    string
	Reason      string // "cross" | "matrix"
	WantEnvJSON string // "" ⇒ NULL (any environment)
	Status      string // open | claimed | done
	ClaimedBy   string
	ClaimedAt   time.Time
	CreatedAt   time.Time
}

// PeerRow is one peers-table row (tracker state, TTL-expired).
type PeerRow struct {
	PeerID           string
	Addr             string
	Port             int
	CapabilitiesJSON string // JSON array, e.g. ["CONTAINER_RUN"]
	SampleIDsJSON    string // JSON array of sample ids the peer seeds
	AnnouncedAt      time.Time
	ExpiresAt        time.Time
}

// NetworkCounts are the honest headline numbers behind /v1/stats
// (goal.md §14.5). Estimated values are computed elsewhere and always
// labeled; these are raw counts only.
type NetworkCounts struct {
	Peers           int64 // unexpired tracker peers
	Packages        int64 // distinct (ecosystem, name) pairs
	Symbols         int64 // distinct non-empty symbol families with evidence
	Observations    int64 // total aggregated observation count
	VerifiedSamples int64 // samples at CROSS_PASS or beyond
}

// IdentityRow is one identities-table row (persistent seeder/verifier
// identity; automatic evidence never uses these).
type IdentityRow struct {
	Login        string
	GithubID     int64
	Display      string
	TokenHash    string
	APITokenHash string
	CreatedAt    time.Time
}

// ClusterRow is one failure_clusters-table row.
type ClusterRow struct {
	ID                  int64
	Ecosystem           string
	PackageName         string
	Symbol              string
	Stage               string
	ErrorFingerprint    string
	ErrorCode           string
	ObservationCount    int64
	EnvSummaryJSON      string
	HypothesesJSON      string // [{domain,confidence}] — never a definitive cause
	RegressionCandidate bool
	VersionsJSON        string
	FirstSeen           time.Time
	LastSeen            time.Time
}

// Store is everything csx-server needs from PostgreSQL. Handlers depend on
// this interface, never on pgx, so tests can substitute fakes.
type Store interface {
	// IngestBatches validates and delta-merges observation batches
	// (semantics in merge.go). Invalid batches are reported in rejected and
	// skipped; a storage failure aborts with err.
	IngestBatches(ctx context.Context, batches []domain.ObservationBatch) (accepted int, rejected []RejectedBatch, err error)

	UpsertPackage(ctx context.Context, p PackageRow) error
	GetPackage(ctx context.Context, purl string) (PackageRow, bool, error)
	ListPackageVersions(ctx context.Context, ecosystem, name string) ([]PackageRow, error)

	GetSnapshot(ctx context.Context, purl, symbol string) (snapshotJSON string, ok bool, err error)
	PutSnapshot(ctx context.Context, purl, symbol, snapshotJSON string) error
	// ListSnapshotTargets returns the distinct (purl, symbol) pairs that
	// have aggregated evidence.
	ListSnapshotTargets(ctx context.Context) ([]SnapshotTarget, error)
	EvidenceForTarget(ctx context.Context, purl, symbol string) ([]EvidenceRow, error)

	SaveCase(ctx context.Context, c domain.Case) error
	SaveSample(ctx context.Context, s SampleRow) error
	GetSample(ctx context.Context, sampleID string) (SampleRow, bool, error)
	ListSamples(ctx context.Context, limit int) ([]SampleRow, error)
	SetSampleStatus(ctx context.Context, sampleID, status string) error

	SaveReceipt(ctx context.Context, r ReceiptRow) error
	ReceiptsForSample(ctx context.Context, sampleID string) ([]ReceiptRow, error)

	// OpenJobs lists open verification jobs a peer with the given sandbox
	// capability may claim ("" ⇒ any). Jobs whose want_env pins a
	// sandboxCapability only match that capability.
	OpenJobs(ctx context.Context, capability string, limit int) ([]JobRow, error)
	// JobsForSample lists every verification job (any status) for a sample,
	// oldest first — the aggregation builder uses it to avoid creating
	// duplicate matrix jobs.
	JobsForSample(ctx context.Context, sampleID string) ([]JobRow, error)
	CreateJob(ctx context.Context, j JobRow) (int64, error)
	// ClaimJob atomically moves an open job to claimed; false means someone
	// else got there first (or the job is gone).
	ClaimJob(ctx context.Context, id int64, peerID string) (bool, error)
	CompleteJob(ctx context.Context, id int64) error

	AnnouncePeer(ctx context.Context, p PeerRow) error
	PeersForSample(ctx context.Context, sampleID string) ([]PeerRow, error)
	ExpirePeers(ctx context.Context, now time.Time) (removed int64, err error)

	PutShard(ctx context.Context, key, etag, shardJSON string) error
	GetShard(ctx context.Context, key string) (etag, shardJSON string, ok bool, err error)

	SaveIdentity(ctx context.Context, login string, githubID int64, tokenHash, apiTokenHash string) error
	IdentityByAPIToken(ctx context.Context, apiTokenHash string) (IdentityRow, bool, error)

	UpsertFailureCluster(ctx context.Context, c ClusterRow) error
	ListFailureClusters(ctx context.Context, packageName string) ([]ClusterRow, error)

	SetStatsDaily(ctx context.Context, day string, statsJSON string) error
	GetLatestStats(ctx context.Context) (statsJSON string, ok bool, err error)
	// NetworkCounts computes the raw stats-rollup numbers as of now.
	NetworkCounts(ctx context.Context, now time.Time) (NetworkCounts, error)

	// PurgeDedupOlderThan deletes rotating dedup buckets older than the
	// given number of days (goal.md §14.4: 30). Aggregates keep their
	// accumulated counts; only the bucket↔evidence linkage is erased.
	PurgeDedupOlderThan(ctx context.Context, days int) (removed int64, err error)

	Close()
}
