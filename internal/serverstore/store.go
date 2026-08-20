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

// SnapshotTarget is one (purl, symbol) pair that has either aggregated
// observation evidence or exact version evidence from a signed v2 receipt,
// and therefore needs a compatibility snapshot. Symbol "" is the
// package-level row.
type SnapshotTarget struct {
	PURL   string
	Symbol string
}

// SnapshotRow is one materialized compatibility document. Collection pages
// use this batched shape so filtering by recorded environment does not issue
// one database query per target.
type SnapshotRow struct {
	PURL         string
	Symbol       string
	SnapshotJSON string
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
	// Direct: at least one reporter listed this package in their own
	// manifest rather than receiving it through somebody else's.
	Direct    bool
	FirstSeen time.Time
	LastSeen  time.Time
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
// given time: evidence targets whose rows were touched, author-declared
// packages of samples that changed, and exact package versions established
// by receipts.
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
	ListSnapshots(ctx context.Context) ([]SnapshotRow, error)
	PutSnapshot(ctx context.Context, purl, symbol, snapshotJSON string) error
	// SnapshotKeys lists materialized rows already stored. It is distinct
	// from ListSnapshotTargets, which lists the live source rows that should
	// exist, and lets aggregation retire snapshots whose last source vanished.
	SnapshotKeys(ctx context.Context) ([]SnapshotTarget, error)
	DeleteSnapshots(ctx context.Context, targets []SnapshotTarget) error
	// ChangedSince reports what moved since a point in time, so the
	// aggregation pass can rebuild only what is stale instead of the whole
	// network on every tick.
	ChangedSince(ctx context.Context, since time.Time) (Changes, error)

	// ListSnapshotTargets returns distinct (purl, symbol) pairs established
	// by aggregated evidence or signed v2 receipt resolver output.
	ListSnapshotTargets(ctx context.Context) ([]SnapshotTarget, error)
	// SnapshotUpdatedAt reports, per PURL, when the evidence behind its
	// compatibility snapshot was last seen. It returns one row per package,
	// but it reaches that row by expanding every snapshot document: the cost
	// is a full pass over compatibility_snapshots, not an index scan. Read it
	// through a cache, never once per request.
	SnapshotUpdatedAt(ctx context.Context) (map[string]time.Time, error)
	EvidenceForTarget(ctx context.Context, purl, symbol string) ([]EvidenceRow, error)

	SaveCase(ctx context.Context, c domain.Case) error
	SaveSample(ctx context.Context, s SampleRow) error
	GetSample(ctx context.Context, sampleID string) (SampleRow, bool, error)
	ListSamples(ctx context.Context, limit int) ([]SampleRow, error)
	// ListSamplesPage is ListSamples with an offset, so a caller that means
	// EVERY sample can page instead of quietly reading the newest N.
	ListSamplesPage(ctx context.Context, limit, offset int) ([]SampleRow, error)
	// SamplesForPackages returns live samples naming any of these package
	// patterns ("pkg:npm/axios@%"), so search does not depend on a global
	// newest-N window.
	SamplesForPackages(ctx context.Context, patterns []string, limit int) ([]SampleRow, error)
	// VerifiedSamplesForPackages is the serving-only form used by package
	// pages: every returned sample has at least one contract-PASS receipt.
	// Search keeps using SamplesForPackages so source-only candidates can be
	// graded honestly rather than disappearing from the local resolver.
	VerifiedSamplesForPackages(ctx context.Context, patterns []string, limit int) ([]SampleRow, error)
	// ListVerifiedSamples returns newest non-quarantined samples with an
	// actual contract-PASS receipt. Public measured findings must never be
	// derived from author prose on a source-only upload.
	ListVerifiedSamples(ctx context.Context, limit int) ([]SampleRow, error)
	// SamplesBySeeder lists one seeder's published samples, so their page
	// does not depend on a global newest-N window.
	SamplesBySeeder(ctx context.Context, login string, limit int) ([]SampleRow, error)
	// SetSampleQuarantine hides or restores a sample without deleting the
	// evidence trail behind it.
	SetSampleQuarantine(ctx context.Context, sampleID string, on bool, reason string) error
	SetSampleStatus(ctx context.Context, sampleID, status string) error

	SaveReceipt(ctx context.Context, r ReceiptRow) error
	// SaveReceiptForJob atomically consumes an exact live claim and stores its
	// receipt. false means the claim no longer belongs to that peer/sample;
	// callers must reject rather than attaching evidence to a recycled lease.
	SaveReceiptForJob(ctx context.Context, r ReceiptRow, jobID int64) (bool, error)
	ReceiptsForSample(ctx context.Context, sampleID string) ([]ReceiptRow, error)

	// OpenJobs lists open verification jobs a peer with the given sandbox
	// capability may claim ("" ⇒ any). A non-empty reason restricts the
	// result to that declarative job class ("cross" or "matrix"). Jobs whose
	// want_env pins a sandboxCapability only match that capability. A job for
	// a sample peerID has already filed a receipt on is not offered as CROSS:
	// a peer cannot independently cross-verify its own work. Matrix jobs stay
	// visible sequentially because one pinned worker can measure several exact
	// runtime lines; ClaimJob prevents simultaneous same-peer/sample claims.
	OpenJobs(ctx context.Context, capability, peerID, reason string, limit int) ([]JobRow, error)
	// OpenJobsPage is the same ordered claimable view with an offset. The HTTP
	// layer uses it to skip missing CAS artifacts without letting stale head
	// rows permanently hide valid work.
	OpenJobsPage(ctx context.Context, capability, peerID, reason, verifierOS string, limit, offset int) ([]JobRow, error)
	// JobsForSample lists every verification job (any status) for a sample,
	// oldest first — the aggregation builder uses it to avoid creating
	// duplicate matrix jobs.
	JobsForSample(ctx context.Context, sampleID string) ([]JobRow, error)
	// Job reads one job for receipt-to-claim binding.
	Job(ctx context.Context, id int64) (JobRow, bool, error)
	CreateJob(ctx context.Context, j JobRow) (int64, error)
	// ClaimJob atomically moves an open job to claimed; false means someone
	// else got there first (or the job is gone).
	ClaimJob(ctx context.Context, id int64, peerID string) (bool, error)
	CompleteJob(ctx context.Context, id int64) error
	// CompleteJobsForSample closes only cross jobs a receipt has answered.
	// Matrix jobs are target-specific and are completed by exact id after
	// their claim and requested environment have been checked. The
	// receipt IS the completion, so nothing depends on a peer remembering
	// to call anything else. A cross job is NOT answered by a receipt from
	// the peer that originated the sample — that receipt proves only that
	// the sample still works where it was built.
	CompleteJobsForSample(ctx context.Context, sampleID, peerID string) error

	// RecordAdoption stores one report that an agent applied a sample and
	// what happened to the build afterwards. This is the far end of the
	// loop the product describes, and it had no route to the server at all.
	RecordAdoption(ctx context.Context, r AdoptionRow) error
	// AdoptionSummary counts adoption reports for the stats rollup.
	AdoptionSummary(ctx context.Context) (AdoptionCounts, error)

	// RecordWanted counts anonymous reports that the network had no answer
	// for a package. The QUESTION IS NEVER SENT — only the part of the
	// request that was already public — and one reporter counts once per
	// epoch per row, so nobody can manufacture a ranking by asking twice.
	RecordWanted(ctx context.Context, epoch, anonID string, rows []WantedRow) error
	RecordWantedBatch(ctx context.Context, reports []WantedSubmission) error
	// TopWanted lists the most-asked packages that still have no sample.
	TopWanted(ctx context.Context, limit int) ([]WantedRow, error)
	// ListWanted returns one searchable, ranked page of unanswered package
	// coordinates plus the full filtered count. TopWanted remains the small
	// public-API view; this is the collection contract used by the website.
	ListWanted(ctx context.Context, query string, offset, limit int) (rows []WantedRow, total int, err error)
	// WantedForPackage is the stable targeted lookup behind a wanted-only
	// package page; its result must not depend on the row's global rank.
	WantedForPackage(ctx context.Context, ecosystem, name string) ([]WantedRow, error)

	AnnouncePeer(ctx context.Context, p PeerRow) error
	PeersForSample(ctx context.Context, sampleID string) ([]PeerRow, error)
	ExpirePeers(ctx context.Context, now time.Time) (removed int64, err error)

	PutShard(ctx context.Context, key, etag, shardJSON string) error
	// ShardKeys lists every shard key currently stored, so a full
	// aggregation pass can tell which shards no longer have any live data
	// behind them. Without it a shard whose last sample was quarantined
	// simply stopped being rebuilt, and kept serving the withdrawn sample.
	ShardKeys(ctx context.Context) ([]string, error)
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
