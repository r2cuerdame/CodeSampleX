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
	// Quarantined hides a sample from every serving read while leaving its
	// receipts and case intact, so the action is reversible and auditable.
	Quarantined      bool
	QuarantineReason string
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
// Changes is everything that could have altered a materialized view since a
// given time: evidence targets whose rows were touched, and the packages of
// samples that were added or that gained a receipt.
//
// Receipts are the complete signal for sample state: SetSampleStatus is
// only ever called from the receipt handler, so a status transition always
// has a receipt behind it and never needs its own timestamp column.
type Changes struct {
	Targets     []SnapshotTarget
	SamplePURLs []string
}

// Empty reports that nothing moved, so a pass has no work beyond stats.
func (c Changes) Empty() bool { return len(c.Targets) == 0 && len(c.SamplePURLs) == 0 }

type NetworkCounts struct {
	// Peers is the distinct anonymous peer buckets that contributed
	// evidence in the current epoch: who is actually using the network
	// today. Buckets rotate daily (goal.md §8.6), so a longer window
	// would count one person many times.
	Peers int64
	// ProjectsMonth is the distinct project buckets seen this calendar
	// month. Those buckets rotate MONTHLY rather than daily, so counting
	// them over the month is the longest honest participation window the
	// identity scheme allows — a peer id summed across days would count
	// one person once per day and report thirty where there is one.
	ProjectsMonth   int64
	Packages        int64 // distinct (ecosystem, name) pairs
	Symbols         int64 // distinct non-empty symbol families with evidence
	Observations    int64 // total aggregated observation count
	VerifiedSamples int64 // contract-verified or independently reproduced
	// ServingPeers is the P2P blob tracker population — nodes that opted
	// into peerListen. Most peers contribute evidence without ever
	// serving blobs, so this is much smaller and is not the headline.
	ServingPeers int64
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
	// ChangedSince reports what moved since a point in time, so the
	// aggregation pass can rebuild only what is stale instead of the whole
	// network on every tick.
	ChangedSince(ctx context.Context, since time.Time) (Changes, error)

	// ListSnapshotTargets returns the distinct (purl, symbol) pairs that
	// have aggregated evidence.
	ListSnapshotTargets(ctx context.Context) ([]SnapshotTarget, error)
	EvidenceForTarget(ctx context.Context, purl, symbol string) ([]EvidenceRow, error)

	SaveCase(ctx context.Context, c domain.Case) error
	SaveSample(ctx context.Context, s SampleRow) error
	GetSample(ctx context.Context, sampleID string) (SampleRow, bool, error)
	ListSamples(ctx context.Context, limit int) ([]SampleRow, error)
	// SamplesForPackages returns live samples naming any of these package
	// patterns ("pkg:npm/axios@%"), so search does not depend on a global
	// newest-N window.
	SamplesForPackages(ctx context.Context, patterns []string, limit int) ([]SampleRow, error)
	// SetSampleQuarantine hides or restores a sample without deleting the
	// evidence trail behind it.
	SetSampleQuarantine(ctx context.Context, sampleID string, on bool, reason string) error
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
	// HotShardKeys lists the shard keys worth warming first, most active
	// first. A fresh install has no local package history, so this is the
	// only thing that fills its cache before the first search.
	HotShardKeys(ctx context.Context, limit int) ([]string, error)

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
