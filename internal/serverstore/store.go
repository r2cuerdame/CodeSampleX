// Package serverstore is the csx-server persistence layer: a Store interface
// over PostgreSQL (plan contract C4) plus the pure ingest logic — batch
// validation and delta-merge dedup semantics — which unit-tests without a
// database. This file must stay free of pgx imports; the pgx implementation
// lives in pg.go.
package serverstore

import (
	"context"
	"strings"
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
	TerminationKind      string
	ExitCode             *int
	Signal               string
	TimeoutMillis        int64
	ErrorSummary         string
	EvidenceQuality      string
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

// SampleCursor is a position in a newest-first sample listing, used to page
// through one without offsets. Offsets shift under concurrent publishing and
// hand the next page a row the previous one already returned; a keyset on
// (created_at, sample_id) cannot, because sample_id is the primary key and
// the pair is therefore unique and totally ordered.
//
// The zero value means "from the newest".
type SampleCursor struct {
	CreatedAt time.Time
	SampleID  string
}

// IsZero reports whether the cursor is the start of a listing.
func (c SampleCursor) IsZero() bool { return c.SampleID == "" }

// sampleEpoch is what a missing created_at sorts as, mirroring the SQL's
// COALESCE(created_at, 'epoch'). created_at is nullable, and a NULL in the
// keyset comparison would evaluate to NULL and silently end the paging.
var sampleEpoch = time.Unix(0, 0).UTC()

// CursorFor returns the keyset position of a row, so a caller can ask for
// the page after it.
func CursorFor(r SampleRow) SampleCursor {
	at := r.CreatedAt
	if at.IsZero() {
		at = sampleEpoch
	}
	return SampleCursor{CreatedAt: at, SampleID: r.SampleID}
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

// JobStatusUnsupported is work no verifier lane in this build can run.
//
// It is not "open", because nothing can claim it, and it is not "done",
// because nothing measured the sample. Production spent three days with an
// open cross queue every worker skipped in silence; a job that no image can
// serve now says so in the one field an operator already reads.
const JobStatusUnsupported = "unsupported"

// JobRow is one verification_jobs-table row.
type JobRow struct {
	ID          int64
	SampleID    string
	Reason      string // "cross" | "matrix"
	WantEnvJSON string // "" ⇒ NULL (any environment)
	Status      string // open | claimed | done | unsupported
	ClaimedBy   string
	ClaimedAt   time.Time
	CreatedAt   time.Time
}

// ContractWasJudged reports whether a receipt reached a verdict on the
// sample, as opposed to never getting far enough to run the contract.
//
// A cross job is hidden from any peer that already filed a receipt for that
// sample, and rightly so: a peer that judged a sample must not judge it
// again and manufacture its own independence. But the queue treated every
// receipt alike, and the two are not alike.
//
// One farm node mounted an empty workspace into its container — systemd
// PrivateTmp — so every run died at the first stage with
// {"resolve":"FAIL","contract":"SKIPPED"}. 167 receipts were written that
// way. The samples were fine; pulled by hand into the same image under the
// same constraints they exit 0. The infrastructure was fixed and that peer
// still could not touch any of them: sixteen of seventeen open cross jobs
// were invisible to it, the oldest unclaimed for over two hours. Deleting
// the receipts by hand cleared it in minutes, which is first aid.
//
// A receipt that never ran the contract says nothing about the sample. It
// says something about the verifier that night. It is kept — it is signed
// evidence and the audit trail is the point — and it stops locking anyone
// out.
func ContractWasJudged(contractResult string) bool {
	switch strings.ToUpper(strings.TrimSpace(contractResult)) {
	case "PASS", "FAIL":
		return true
	}
	return false
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
	ID                    int64
	Ecosystem             string
	PackageName           string
	Symbol                string
	Stage                 string
	ErrorFingerprint      string
	ErrorCode             string
	TerminationKind       string
	ExitCode              *int
	Signal                string
	TimeoutMillis         int64
	ErrorSummary          string
	EvidenceQuality       string
	ObservationCount      int64
	EnvSummaryJSON        string
	EnvVariantsJSON       string
	EvidenceBreakdownJSON string
	HypothesesJSON        string // [{domain,confidence}] — never a definitive cause
	RegressionCandidate   bool
	DiagnosticCandidate   bool
	VersionsJSON          string
	FirstSeen             time.Time
	LastSeen              time.Time
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
	//
	// This is a NEWEST-N window. Never answer a question about the whole
	// corpus with it: a serving read that filters inside a fixed window
	// shrinks as the corpus grows, which is how the findings page fell from
	// 543 entries to 250 while nothing was taken down. For findings, use
	// ListVerifiedBeliefSamples, which pages the eligible set instead.
	ListVerifiedSamples(ctx context.Context, limit int) ([]SampleRow, error)
	// ListVerifiedBeliefSamples pages the samples that could be findings:
	// non-quarantined, holding an actual contract-PASS receipt, and stating
	// in their own manifest what was believed.
	//
	// The belief is what narrows the read, and it is applied in the store so
	// the caller never has to choose between reading everything and reading
	// a recent slice of everything. Rows come back newest first, starting
	// strictly after `after`; the zero cursor starts at the newest. `limit`
	// bounds ONE read, not the answer — a caller wanting every finding pages
	// until a short page comes back.
	ListVerifiedBeliefSamples(ctx context.Context, after SampleCursor, limit int) ([]SampleRow, error)
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
	// CrossJobsForLaneReview lists cross jobs whose requirements can still be
	// re-checked against the verifier images this build actually pins: the
	// open ones, the unsupported ones, and claims that outlived their lease.
	// A live claim is left alone — its receipt is bound to the requirements
	// the worker was handed.
	CrossJobsForLaneReview(ctx context.Context, limit int) ([]JobRow, error)
	// SetJobRequirements rewrites one job's requirements and status, and
	// releases any claim on it. Jobs created before the fleet's lanes were
	// known are otherwise unreachable: nothing in the request path ever looks
	// at a job again once it is open.
	SetJobRequirements(ctx context.Context, id int64, wantEnvJSON, status string) error
	CreateJob(ctx context.Context, j JobRow) (int64, error)
	// ClaimJob atomically moves an open job to claimed; false means someone
	// else got there first (or the job is gone).
	ClaimJob(ctx context.Context, id int64, peerID string) (bool, error)
	CompleteJob(ctx context.Context, id int64) error
	// VersionCoresidence lists the version pairs of one library that a
	// scanner saw in a SINGLE resolution — the fact the server cannot infer,
	// because a batch carries one package and a lockfile arrives shredded.
	VersionCoresidence(ctx context.Context, ecosystem, name string) ([]VersionCoresidence, error)
	// Dependants lists what pulled each version of one library — the other
	// half of a co-residence, and the half a person can act on.
	Dependants(ctx context.Context, ecosystem, name string) ([]DependencyEdge, error)
	// Dependencies lists what shipped ALONGSIDE each version of one package:
	// upgrade a library and its dependencies move under you, and the one that
	// moved is usually the one that broke the build.
	Dependencies(ctx context.Context, ecosystem, name string) ([]DependencyEdge, error)
	// StrandedDrafts lists quarantined authoring drafts with no verification
	// left to wait for: no passing receipt, no open or claimed job, and fewer
	// than maxAttempts cross jobs already spent. They are what a verifier
	// that cannot resolve leaves behind, and without this they are invisible
	// — verified by nobody and waiting on nothing.
	StrandedDrafts(ctx context.Context, maxAttempts, limit int) ([]string, error)
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
	// RecordSearchHit counts one search that found something. It is the
	// denominator adoptions never had: the network could see the demand
	// it could not satisfy — a miss uploads a Wanted ask — and nothing
	// of the demand it could.
	RecordSearchHit(ctx context.Context, r SearchHitRow) error
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
	// GetShardEtag reads the ETag without loading the document, so a
	// revalidation costs what the limiter's 304 refund assumes it costs.
	GetShardEtag(ctx context.Context, key string) (etag string, ok bool, err error)
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
