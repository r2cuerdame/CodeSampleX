package serverstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/r2cuerdame/codesamplex/internal/activity"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// PG implements Store on PostgreSQL via pgx/v5.
//
// It manages its own small *pgx.Conn pool (connPool below) instead of
// pgxpool: the repo pins its dependency set and pgxpool's puddle dependency
// is not part of it. The pool is deliberately tiny — the production target
// is a 2GB VM with max_connections 40.
type PG struct {
	pool *connPool
}

var _ Store = (*PG)(nil)

// defaultMaxConns caps concurrent PostgreSQL connections per process.
const defaultMaxConns = 8

// Open connects to PostgreSQL and returns a ready Store. It validates the
// DSN and dials one connection eagerly so misconfiguration fails fast.
func Open(ctx context.Context, dsn string) (*PG, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("serverstore: parse dsn: %w", err)
	}
	p := newPG(cfg)
	c, err := p.pool.acquire(ctx)
	if err != nil {
		return nil, err
	}
	p.pool.release(c)
	return p, nil
}

func newPG(cfg *pgx.ConnConfig) *PG {
	return &PG{pool: newConnPool(cfg, defaultMaxConns)}
}

// Close releases every pooled connection.
func (p *PG) Close() { p.pool.close() }

// Migrate applies the embedded migrations (see migrate.go).
func (p *PG) Migrate(ctx context.Context) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		return Migrate(ctx, c)
	})
}

func (p *PG) withConn(ctx context.Context, fn func(*pgx.Conn) error) error {
	c, err := p.pool.acquire(ctx)
	if err != nil {
		return err
	}
	defer p.pool.release(c)
	return fn(c)
}

// ---------------------------------------------------------------- ingest --

// IngestBatches validates each batch and delta-merges the valid ones with
// the semantics documented in merge.go: per (bucket, agg row, epoch) only
// count growth is added, so a re-sent identical batch adds exactly 0.
// IngestBatches applies a whole request in ONE transaction. Per-batch
// transactions meant a 500-batch upload paid 500 commits: on a small
// instance that took longer than the client's timeout, so a first sync
// after scanning a machine could never finish. One commit is also the
// honest unit — a request lands completely or not at all, and the
// delta-merge makes a retried request add nothing twice.
func (p *PG) IngestBatches(ctx context.Context, batches []domain.ObservationBatch) (int, []RejectedBatch, error) {
	accepted := 0
	var rejected []RejectedBatch
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

		accepted = 0
		rejected = nil
		for i, b := range batches {
			if verr := ValidateBatch(b); verr != nil {
				rejected = append(rejected, RejectedBatch{Index: i, Reason: verr.Error()})
				continue
			}
			if err := ingestOne(ctx, tx, b); err != nil {
				return fmt.Errorf("serverstore: ingest batch %d: %w", i, err)
			}
			accepted++
		}
		return tx.Commit(ctx)
	})
	return accepted, rejected, err
}

func ingestOne(ctx context.Context, tx pgx.Tx, b domain.ObservationBatch) error {
	purl, err := domain.ParsePURL(b.Package) // already validated
	if err != nil {
		return err
	}
	canonical := purl.String()
	env := b.Environment.Normalize()
	envJSON := domain.MustCanonicalJSON(env)
	confidence := string(b.SymbolConfidence)
	if confidence == "" {
		confidence = string(domain.SymbolUnknown)
	}

	// Keep the packages registry aware of every purl with evidence.
	// Publicness stays UNKNOWN until the registry check upgrades it.
	if _, err := tx.Exec(ctx, `
		INSERT INTO packages(purl, ecosystem, name, version, major)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (purl) DO UPDATE SET last_seen = now()`,
		canonical, purl.Ecosystem, purl.Name, purl.Version, purl.Major()); err != nil {
		return err
	}

	var aggID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO evidence_agg
			(purl, symbol, symbol_confidence, env_hash, env_json, stage, result, error_fp, error_code, direct)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (purl, symbol, env_hash, stage, result, error_fp) DO UPDATE SET
			last_seen = now(),
			symbol_confidence = EXCLUDED.symbol_confidence,
			error_code = CASE WHEN evidence_agg.error_code = ''
				THEN EXCLUDED.error_code ELSE evidence_agg.error_code END,
			-- Chosen wins and never unsays itself: one project resolving a
			-- package transitively does not undo another that listed it.
			direct = evidence_agg.direct OR EXCLUDED.direct
		RETURNING id`,
		canonical, b.Symbol, confidence, env.Hash(), []byte(envJSON),
		string(b.Stage), string(b.Result), b.ErrorFingerprint, b.ErrorCode, b.Direct,
	).Scan(&aggID); err != nil {
		return err
	}

	// Who pulled what, one row per (edge, project, day). The server cannot
	// derive this: a batch carries one package, so a resolution arrives
	// already shredded.
	if b.ProjectBucket != "" {
		for _, pair := range edgeClaims(b) {
			parent, child := pair[0], pair[1]
			if _, err := tx.Exec(ctx, `
				INSERT INTO dependency_edge
					(ecosystem, parent_name, parent_version, child_name, child_version, bucket, epoch)
				VALUES($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (ecosystem, parent_name, parent_version, child_name, child_version, bucket, epoch)
				DO UPDATE SET last_seen = now()`,
				parent.Ecosystem, parent.Name, parent.Version,
				child.Name, child.Version, b.ProjectBucket, b.Epoch); err != nil {
				return err
			}
		}
	}

	// The pair the scanner saw, one row per (pair, project, day). Recorded
	// even when nothing failed: a pair that never breaks is worth knowing
	// about, and the reader decides what that means.
	if b.ProjectBucket != "" {
		failed := batchNamesAnAttributedFailure(b)
		for _, pair := range coresidencePairs(b) {
			if _, err := tx.Exec(ctx, `
				INSERT INTO version_coresidence
					(ecosystem, name, lower_version, higher_version, bucket, epoch, failing)
				VALUES($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (ecosystem, name, lower_version, higher_version, bucket, epoch)
				DO UPDATE SET failing = version_coresidence.failing OR EXCLUDED.failing,
				              last_seen = now()`,
				purl.Ecosystem, purl.Name, pair.Lower, pair.Higher,
				b.ProjectBucket, b.Epoch, failed); err != nil {
				return err
			}
		}
	}

	// The peer bucket's previous contribution for this agg row + epoch
	// decides the delta; FOR UPDATE serializes concurrent re-sends.
	incoming := int64(b.ObservationCount)
	var prev int64
	err = tx.QueryRow(ctx, `
		SELECT count FROM evidence_dedup
		WHERE bucket_kind='peer' AND bucket=$1 AND agg_id=$2 AND epoch=$3
		FOR UPDATE`,
		b.AnonID, aggID, b.Epoch).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		prev = 0
	} else if err != nil {
		return err
	}
	delta := deltaContribution(prev, incoming)

	for _, bk := range []struct{ kind, bucket string }{
		{"peer", b.AnonID},
		{"project", b.ProjectBucket},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO evidence_dedup(bucket_kind, bucket, agg_id, epoch, count)
			VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (bucket_kind, bucket, agg_id, epoch)
			DO UPDATE SET count = EXCLUDED.count`,
			bk.kind, bk.bucket, aggID, b.Epoch, incoming); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE evidence_agg SET
			observation_count = observation_count + $2,
			-- Buckets are counted WITHIN an epoch and never across them.
			-- The anonID rotates daily and the project bucket monthly, so
			-- COUNT(DISTINCT bucket) over the whole ledger made one machine
			-- reporting on N days look like N independent peers: the
			-- network's central claim inflating on its own, with nobody
			-- doing anything. The peak over a single epoch is the strongest
			-- independence a rotating identity can support.
			unique_peer_buckets = (SELECT COALESCE(MAX(c), 0) FROM (
				SELECT COUNT(DISTINCT bucket) AS c FROM evidence_dedup
				 WHERE agg_id = $1 AND bucket_kind = 'peer'
				 GROUP BY epoch) pk),
			unique_project_buckets = (SELECT COALESCE(MAX(c), 0) FROM (
				SELECT COUNT(DISTINCT bucket) AS c FROM evidence_dedup
				 WHERE agg_id = $1 AND bucket_kind = 'project'
				 GROUP BY epoch) pj),
			last_seen = now()
		WHERE id = $1`, aggID, delta); err != nil {
		return err
	}
	// The caller owns the transaction: one commit per request, not per batch.
	return nil
}

// PurgeDedupOlderThan deletes dedup buckets whose epoch day is older than
// the retention window. Aggregate counts are kept — only the rotating
// bucket linkage is erased (goal.md §14.4), so unique_*_buckets freeze at
// their accumulated values rather than shrinking retroactively.
func (p *PG) PurgeDedupOlderThan(ctx context.Context, days int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	var removed int64
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx, `DELETE FROM evidence_dedup WHERE epoch < $1`, cutoff)
		if err != nil {
			return fmt.Errorf("serverstore: purge dedup: %w", err)
		}
		removed = tag.RowsAffected()
		return nil
	})
	return removed, err
}

// -------------------------------------------------------------- packages --

func (p *PG) UpsertPackage(ctx context.Context, pkg PackageRow) error {
	var checked *time.Time
	if !pkg.CheckedAt.IsZero() {
		checked = &pkg.CheckedAt
	}
	if pkg.Publicness == "" {
		pkg.Publicness = "UNKNOWN"
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO packages(purl, ecosystem, name, version, major, publicness, checked_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (purl) DO UPDATE SET
				publicness = EXCLUDED.publicness,
				checked_at = EXCLUDED.checked_at,
				last_seen = now()`,
			pkg.PURL, pkg.Ecosystem, pkg.Name, pkg.Version, pkg.Major, pkg.Publicness, checked)
		return err
	})
}

const packageCols = `purl, ecosystem, name, version, major, publicness, checked_at, first_seen, last_seen`

func scanPackage(row pgx.Row) (PackageRow, error) {
	var pkg PackageRow
	var checked, first, last *time.Time
	err := row.Scan(&pkg.PURL, &pkg.Ecosystem, &pkg.Name, &pkg.Version,
		&pkg.Major, &pkg.Publicness, &checked, &first, &last)
	if err != nil {
		return PackageRow{}, err
	}
	if checked != nil {
		pkg.CheckedAt = *checked
	}
	if first != nil {
		pkg.FirstSeen = *first
	}
	if last != nil {
		pkg.LastSeen = *last
	}
	return pkg, nil
}

func (p *PG) GetPackage(ctx context.Context, purl string) (PackageRow, bool, error) {
	var pkg PackageRow
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		row := c.QueryRow(ctx, `SELECT `+packageCols+` FROM packages WHERE purl=$1`, purl)
		got, err := scanPackage(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		pkg, found = got, true
		return nil
	})
	return pkg, found, err
}

func (p *PG) ListPackageVersions(ctx context.Context, ecosystem, name string) ([]PackageRow, error) {
	var out []PackageRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT `+packageCols+` FROM packages
			WHERE ecosystem=$1 AND name=$2
			ORDER BY last_seen DESC, version DESC`, ecosystem, name)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			pkg, err := scanPackage(rows)
			if err != nil {
				return err
			}
			out = append(out, pkg)
		}
		return rows.Err()
	})
	return out, err
}

// ------------------------------------------------------------- snapshots --

func (p *PG) GetSnapshot(ctx context.Context, purl, symbol string) (string, bool, error) {
	var js string
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		err := c.QueryRow(ctx, `
			SELECT snapshot::text FROM compatibility_snapshots
			WHERE purl=$1 AND symbol=$2`, purl, symbol).Scan(&js)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return js, found, err
}

func (p *PG) ListSnapshots(ctx context.Context) ([]SnapshotRow, error) {
	var out []SnapshotRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT purl, symbol, snapshot::text
			FROM compatibility_snapshots
			ORDER BY purl, symbol`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row SnapshotRow
			if err := rows.Scan(&row.PURL, &row.Symbol, &row.SnapshotJSON); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) PutSnapshot(ctx context.Context, purl, symbol, snapshotJSON string) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO compatibility_snapshots(purl, symbol, snapshot, generated_at)
			VALUES($1,$2,$3,now())
			ON CONFLICT (purl, symbol) DO UPDATE SET
				snapshot = EXCLUDED.snapshot, generated_at = now()`,
			purl, symbol, []byte(snapshotJSON))
		return err
	})
}

func (p *PG) SnapshotKeys(ctx context.Context) ([]SnapshotTarget, error) {
	var out []SnapshotTarget
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT purl, symbol FROM compatibility_snapshots
			ORDER BY purl, symbol`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var target SnapshotTarget
			if err := rows.Scan(&target.PURL, &target.Symbol); err != nil {
				return err
			}
			out = append(out, target)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) DeleteSnapshots(ctx context.Context, targets []SnapshotTarget) error {
	if len(targets) == 0 {
		return nil
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
		for _, target := range targets {
			if _, err := tx.Exec(ctx, `
				DELETE FROM compatibility_snapshots WHERE purl=$1 AND symbol=$2`,
				target.PURL, target.Symbol); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
}

func (p *PG) SnapshotUpdatedAt(ctx context.Context) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		// The evidence's own recency, not the builder's. generated_at is
		// when the aggregation last WROTE the document, and a full pass
		// rewrites every one of them — ordering by it produced a list where
		// 1,965 packages all claimed the same date. Each snapshot row
		// carries the lastSeen of the evidence behind it; that is the date
		// a reader means by "recently measured". The write time remains the
		// fallback for a document whose rows recorded none.
		rows, err := c.Query(ctx, `
			SELECT s.purl,
			       COALESCE(
			         MAX(CASE WHEN r.value ? 'lastSeen' AND r.value->>'lastSeen' <> ''
			                  THEN (r.value->>'lastSeen')::timestamptz END),
			         MAX(s.generated_at))
			  FROM compatibility_snapshots s
			  LEFT JOIN LATERAL jsonb_array_elements(s.snapshot->'rows') r ON true
			 GROUP BY s.purl`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var purl string
			var at *time.Time
			if err := rows.Scan(&purl, &at); err != nil {
				return err
			}
			if at != nil {
				out[purl] = *at
			}
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) ListSnapshotTargets(ctx context.Context) ([]SnapshotTarget, error) {
	seen := map[SnapshotTarget]bool{}
	// Receipt claims are collected first and attributed together:
	// snapshotTargetsFromClaims needs to see every claim on a symbol before it
	// can tell which is narrowest.
	var claims []receiptClaim
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT DISTINCT purl, symbol FROM evidence_agg`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var t SnapshotTarget
			if err := rows.Scan(&t.PURL, &t.Symbol); err != nil {
				rows.Close()
				return err
			}
			seen[t] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		// Validate the signed list as one unit in Go, using the same canonical
		// PURL/concrete-version/sorted/unique rules as Fake and aggregation.
		// Exploding JSON in SQL first partially accepted malformed receipts.
		receipts, err := c.Query(ctx, `
			SELECT r.receipt::text, s.manifest::text
			FROM samples s JOIN receipts r ON r.sample_id = s.sample_id
			WHERE NOT s.quarantined
			  AND r.receipt->>'schemaVersion' = '2'
			  AND r.receipt->'stages'->>'resolve' = 'PASS'`)
		if err != nil {
			return err
		}
		defer receipts.Close()
		for receipts.Next() {
			var receiptJSON, manifestJSON string
			if err := receipts.Scan(&receiptJSON, &manifestJSON); err != nil {
				return err
			}
			var manifest struct {
				Symbols []string `json:"symbols"`
				Subject string   `json:"subject"`
			}
			if json.Unmarshal([]byte(manifestJSON), &manifest) != nil {
				continue
			}
			claims = append(claims, receiptClaim{
				Packages: resolvedPackageStrings(receiptJSON),
				Symbols:  manifest.Symbols,
				Subject:  manifest.Subject,
			})
		}
		return receipts.Err()
	})
	if err != nil {
		return nil, err
	}
	for _, t := range snapshotTargetsFromClaims(claims) {
		seen[t] = true
	}
	out := make([]SnapshotTarget, 0, len(seen))
	for target := range seen {
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PURL != out[j].PURL {
			return out[i].PURL < out[j].PURL
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
}

// ChangedSince implements the incremental-rebuild query. It includes both
// author-declared purls and exact purls established by v2 receipts; on an
// idle network the timestamp predicates return nothing and aggregation does
// no materialized-view work.
func (p *PG) ChangedSince(ctx context.Context, since time.Time) (Changes, error) {
	var c Changes
	seenPURLs := map[string]bool{}
	err := p.withConn(ctx, func(conn *pgx.Conn) error {
		rows, err := conn.Query(ctx,
			`SELECT DISTINCT purl, symbol FROM evidence_agg WHERE last_seen > $1`, since)
		if err != nil {
			return err
		}
		for rows.Next() {
			var t SnapshotTarget
			if err := rows.Scan(&t.PURL, &t.Symbol); err != nil {
				rows.Close()
				return err
			}
			c.Targets = append(c.Targets, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// A new sample, or a receipt against an existing one — the latter
		// also being the only thing that moves a sample's status.
		prows, err := conn.Query(ctx, `
			SELECT DISTINCT pkg FROM (
				-- created_at covers new samples; updated_at covers every
				-- change made outside the request path (quarantine, a status
				-- corrected by recompute-status), which would otherwise leave
				-- the materialized shard advertising the old state forever.
				SELECT jsonb_array_elements_text(manifest->'packages') AS pkg
				FROM samples WHERE created_at > $1 OR updated_at > $1
				UNION
				SELECT jsonb_array_elements_text(s.manifest->'packages') AS pkg
				FROM samples s JOIN receipts r ON r.sample_id = s.sample_id
				WHERE r.created_at > $1
			) t`, since)
		if err != nil {
			return err
		}
		for prows.Next() {
			var purl string
			if err := prows.Scan(&purl); err != nil {
				prows.Close()
				return err
			}
			seenPURLs[purl] = true
		}
		if err := prows.Err(); err != nil {
			prows.Close()
			return err
		}
		prows.Close()

		// Validate each historical/new receipt as a whole before any member
		// becomes a dirty key. This keeps malformed direct DB inserts from
		// affecting one convenient version in PostgreSQL but not in Fake.
		rrows, err := conn.Query(ctx, `
			SELECT r.receipt::text
			FROM receipts r JOIN samples s ON s.sample_id = r.sample_id
			WHERE r.created_at > $1 OR s.created_at > $1 OR s.updated_at > $1`, since)
		if err != nil {
			return err
		}
		defer rrows.Close()
		for rrows.Next() {
			var receiptJSON string
			if err := rrows.Scan(&receiptJSON); err != nil {
				return err
			}
			for _, purl := range resolvedPackageStrings(receiptJSON) {
				seenPURLs[purl] = true
			}
		}
		return rrows.Err()
	})
	if err != nil {
		return Changes{}, err
	}
	for purl := range seenPURLs {
		c.SamplePURLs = append(c.SamplePURLs, purl)
	}
	sort.Strings(c.SamplePURLs)
	sort.Slice(c.Targets, func(i, j int) bool {
		if c.Targets[i].PURL != c.Targets[j].PURL {
			return c.Targets[i].PURL < c.Targets[j].PURL
		}
		return c.Targets[i].Symbol < c.Targets[j].Symbol
	})
	return c, nil
}

func (p *PG) EvidenceForTarget(ctx context.Context, purl, symbol string) ([]EvidenceRow, error) {
	var out []EvidenceRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT purl, symbol, symbol_confidence, env_hash, env_json::text,
			       stage, result, error_fp, error_code, observation_count,
			       unique_peer_buckets, unique_project_buckets, first_seen, last_seen
			FROM evidence_agg
			WHERE purl=$1 AND symbol=$2
			ORDER BY env_hash, stage, result, error_fp`, purl, symbol)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e EvidenceRow
			var first, last *time.Time
			if err := rows.Scan(&e.PURL, &e.Symbol, &e.SymbolConfidence, &e.EnvHash,
				&e.EnvJSON, &e.Stage, &e.Result, &e.ErrorFingerprint, &e.ErrorCode,
				&e.ObservationCount, &e.UniquePeerBuckets, &e.UniqueProjectBuckets,
				&first, &last); err != nil {
				return err
			}
			if first != nil {
				e.FirstSeen = *first
			}
			if last != nil {
				e.LastSeen = *last
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// -------------------------------------------------------- cases + samples --

func (p *PG) SaveCase(ctx context.Context, cse domain.Case) error {
	if cse.CaseID == "" {
		cse.CaseID = cse.ComputeID()
	}
	js := domain.MustCanonicalJSON(cse)
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO cases(case_id, kind, goal, json)
			VALUES($1,$2,$3,$4)
			ON CONFLICT (case_id) DO UPDATE SET
				kind = EXCLUDED.kind, goal = EXCLUDED.goal, json = EXCLUDED.json`,
			cse.CaseID, cse.Kind, cse.Goal, []byte(js))
		return err
	})
}

func (p *PG) SaveSample(ctx context.Context, s SampleRow) error {
	if s.Status == "" {
		s.Status = "PUBLISHED"
	}
	if s.License == "" {
		s.License = "MIT-0"
	}
	var caseID *string
	if s.CaseID != "" {
		caseID = &s.CaseID
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO samples(sample_id, case_id, manifest, status, origin_seeder,
				license, size_bytes, hot_score, quarantined, quarantine_reason)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (sample_id) DO UPDATE SET
				manifest = EXCLUDED.manifest,
				-- NOT the status. A sample id is the sha256 of its content,
				-- so a conflict means this exact sample is already here --
				-- and the ingest path always sends "PUBLISHED". Overwriting
				-- with it threw away CROSS_PASS, MATRIX_PASS or STABLE that
				-- independent peers had actually earned, on nothing more
				-- than the author re-running their publish. The receipts
				-- survived, so the status was recoverable only by an
				-- operator running recompute-status by hand; until then the
				-- sample ranked lower everywhere and could be cut from its
				-- own shard by the sample cap.
				--
				-- Status is derived from receipts. SetSampleStatus is how it
				-- moves; this is not.
				hot_score = EXCLUDED.hot_score`,
			s.SampleID, caseID, []byte(s.ManifestJSON), s.Status, s.OriginSeeder,
			s.License, s.SizeBytes, s.HotScore, s.Quarantined, s.QuarantineReason)
		return err
	})
}

const sampleCols = `sample_id, COALESCE(case_id,''), manifest::text, status,
	COALESCE(origin_seeder,''), license, size_bytes, hot_score, created_at,
	quarantined, COALESCE(quarantine_reason,'')`

func scanSample(row pgx.Row) (SampleRow, error) {
	var s SampleRow
	var created *time.Time
	err := row.Scan(&s.SampleID, &s.CaseID, &s.ManifestJSON, &s.Status,
		&s.OriginSeeder, &s.License, &s.SizeBytes, &s.HotScore, &created,
		&s.Quarantined, &s.QuarantineReason)
	if err != nil {
		return SampleRow{}, err
	}
	if created != nil {
		s.CreatedAt = *created
	}
	return s, nil
}

func (p *PG) GetSample(ctx context.Context, sampleID string) (SampleRow, bool, error) {
	var s SampleRow
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		row := c.QueryRow(ctx, `SELECT `+sampleCols+` FROM samples WHERE sample_id=$1`, sampleID)
		got, err := scanSample(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		s, found = got, true
		return nil
	})
	return s, found, err
}

// SamplesForPackages returns non-quarantined samples whose manifest names
// any of the given package (ecosystem, name) pairs.
//
// Search used to take the newest 500 samples globally and score those,
// which made relevance a function of publication order: at 501 samples the
// oldest silently stop being findable no matter how good they are, and
// anyone able to publish 500 rows owns every result. Filtering in SQL makes
// the limit per-query instead of global.
func (p *PG) SamplesForPackages(ctx context.Context, names []string, limit int) ([]SampleRow, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	var out []SampleRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT `+sampleCols+` FROM samples
			WHERE NOT quarantined
			  AND EXISTS (
				SELECT 1 FROM jsonb_array_elements_text(manifest->'packages') AS pkg
				WHERE pkg LIKE ANY($1)
			  )
			ORDER BY created_at DESC, sample_id LIMIT $2`, names, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			s, serr := scanSample(rows)
			if serr != nil {
				return serr
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// VerifiedSamplesForPackages is SamplesForPackages with a receipt-backed
// contract proof. Publication alone is not verification.
func (p *PG) VerifiedSamplesForPackages(ctx context.Context, names []string, limit int) ([]SampleRow, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	var out []SampleRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT `+sampleCols+` FROM samples
			WHERE NOT quarantined
			  AND EXISTS (
				SELECT 1 FROM receipts verified_receipt
				WHERE verified_receipt.sample_id = samples.sample_id
				  AND verified_receipt.contract_result = 'PASS'
			  )
			  AND EXISTS (
				SELECT 1 FROM jsonb_array_elements_text(manifest->'packages') AS pkg
				WHERE pkg LIKE ANY($1)
			  )
			ORDER BY created_at DESC, sample_id LIMIT $2`, names, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			s, serr := scanSample(rows)
			if serr != nil {
				return serr
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) ListVerifiedSamples(ctx context.Context, limit int) ([]SampleRow, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []SampleRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT `+sampleCols+` FROM samples
			WHERE NOT quarantined
			  AND EXISTS (
				SELECT 1 FROM receipts verified_receipt
				WHERE verified_receipt.sample_id = samples.sample_id
				  AND verified_receipt.contract_result = 'PASS'
			  )
			ORDER BY created_at DESC, sample_id LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			s, serr := scanSample(rows)
			if serr != nil {
				return serr
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// SamplesBySeeder lists a seeder's own published samples, newest first.
//
// The seeder page used to read the newest 500 samples network-wide and
// filter them by login, so a seeder's older work disappeared from their own
// page the moment the network published 500 samples after it — silently,
// and first for the people who contributed earliest.
func (p *PG) SamplesBySeeder(ctx context.Context, login string, limit int) ([]SampleRow, error) {
	if login == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	var out []SampleRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT `+sampleCols+` FROM samples
			WHERE NOT quarantined AND origin_seeder = $1
			ORDER BY created_at DESC, sample_id LIMIT $2`, login, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			s, serr := scanSample(rows)
			if serr != nil {
				return serr
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// SetSampleQuarantine hides or restores a sample. Evidence, receipts and
// the case row are left untouched: a quarantine must be reversible and
// auditable, not a delete.
func (p *PG) SetSampleQuarantine(ctx context.Context, sampleID string, on bool, reason string) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx, `
			UPDATE samples
			SET quarantined = $2,
			    quarantine_reason = CASE WHEN $2 THEN $3 ELSE NULL END,
			    quarantined_at = CASE WHEN $2 THEN now() ELSE NULL END,
			    updated_at = now()
			WHERE sample_id = $1`, sampleID, on, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("serverstore: no sample %s", sampleID)
		}
		return nil
	})
}

func (p *PG) ListSamples(ctx context.Context, limit int) ([]SampleRow, error) {
	return p.ListSamplesPage(ctx, limit, 0)
}

// ListSamplesPage is ListSamples with an offset, so a caller that must
// visit EVERY sample can walk past the limit.
//
// recompute-status promised to "re-derive every sample status" and read one
// capped page, so on a network past the cap it silently skipped the rest.
// Warning about it was not enough either: the list is ordered newest-first,
// so the "re-run until the count drops" advice returned the identical page
// every time and could never converge.
func (p *PG) ListSamplesPage(ctx context.Context, limit, offset int) ([]SampleRow, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var out []SampleRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT `+sampleCols+` FROM samples
			WHERE NOT quarantined
			ORDER BY created_at DESC, sample_id LIMIT $1 OFFSET $2`, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			s, err := scanSample(rows)
			if err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) SetSampleStatus(ctx context.Context, sampleID, status string) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx,
			`UPDATE samples SET status=$2, updated_at=now() WHERE sample_id=$1`, sampleID, status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("serverstore: sample %s not found", sampleID)
		}
		return nil
	})
}

// --------------------------------------------------------------- receipts --

func (p *PG) SaveReceipt(ctx context.Context, r ReceiptRow) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO receipts(receipt_id, sample_id, peer_id, env_hash, receipt, contract_result)
			VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT (receipt_id) DO NOTHING`,
			r.ReceiptID, r.SampleID, r.PeerID, r.EnvHash, []byte(r.ReceiptJSON), r.ContractResult)
		return err
	})
}

func (p *PG) SaveReceiptForJob(ctx context.Context, r ReceiptRow, jobID int64) (bool, error) {
	saved := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		tag, err := tx.Exec(ctx, `
			UPDATE verification_jobs SET status='done'
			 WHERE id=$1 AND sample_id=$2 AND status='claimed' AND claimed_by=$3`,
			jobID, r.SampleID, r.PeerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		inserted, err := tx.Exec(ctx, `
			INSERT INTO receipts(receipt_id, sample_id, peer_id, env_hash, receipt, contract_result)
			VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT (receipt_id) DO NOTHING`,
			r.ReceiptID, r.SampleID, r.PeerID, r.EnvHash, []byte(r.ReceiptJSON), r.ContractResult)
		if err != nil {
			return err
		}
		if inserted.RowsAffected() != 1 {
			return nil // rollback the job update; this receipt answered another job already
		}
		// A designated sample author already proved LOCAL_PASS before upload.
		// The claimed verifier is the independent confirmation. Promote the
		// quarantined draft atomically with its signed PASS receipt so there is
		// no state where an unverified draft is public, or a verified draft is
		// stranded after a process crash.
		//
		// Promotion used to require a live authoring assignment as well. An
		// authoring session expires an hour after its last refresh and its
		// assignment is deleted with it, so a draft verified after that
		// window kept its signed PASS receipt and stayed quarantined
		// forever — verified, and invisible. The draft row still has to
		// exist (this is authoring output, not an anonymous upload), but
		// whether the writing session is still open says nothing about
		// whether the contract ran.
		if r.ContractResult == "PASS" {
			if _, err := tx.Exec(ctx, `UPDATE samples
				SET status='CROSS_PASS', quarantined=false, quarantine_reason=NULL, updated_at=now()
				WHERE sample_id=$1 AND status='DRAFT' AND quarantined
				  AND EXISTS(SELECT 1 FROM verification_jobs WHERE id=$2 AND reason='cross')
				  AND EXISTS(SELECT 1 FROM authoring_drafts d
				    WHERE d.sample_id=samples.sample_id)`, r.SampleID, jobID); err != nil {
				return err
			}
		} else if r.ContractResult == "FAIL" {
			if _, err := tx.Exec(ctx, `DELETE FROM authoring_assignments
				WHERE sample_id=$1 AND EXISTS(
					SELECT 1 FROM verification_jobs WHERE id=$2 AND reason='cross')`, r.SampleID, jobID); err != nil {
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		saved = true
		return nil
	})
	return saved, err
}

func (p *PG) ReceiptsForSample(ctx context.Context, sampleID string) ([]ReceiptRow, error) {
	var out []ReceiptRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT receipt_id, sample_id, peer_id, env_hash, receipt::text,
			       COALESCE(contract_result,''), created_at
			FROM receipts WHERE sample_id=$1
			ORDER BY created_at, receipt_id`, sampleID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ReceiptRow
			var created *time.Time
			if err := rows.Scan(&r.ReceiptID, &r.SampleID, &r.PeerID, &r.EnvHash,
				&r.ReceiptJSON, &r.ContractResult, &created); err != nil {
				return err
			}
			if created != nil {
				r.CreatedAt = *created
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ------------------------------------------------------------------- jobs --

func (p *PG) CreateJob(ctx context.Context, j JobRow) (int64, error) {
	var wantEnv []byte
	if j.WantEnvJSON != "" {
		wantEnv = []byte(j.WantEnvJSON)
	}
	var id int64
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		if j.Reason != "matrix" {
			return c.QueryRow(ctx, `
			INSERT INTO verification_jobs(sample_id, reason, want_env)
			VALUES($1,$2,$3) RETURNING id`,
				j.SampleID, j.Reason, wantEnv).Scan(&id)
		}
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		// Builder passes can overlap during deployment. Serialize the exact
		// sample+matrix target before the read/create pair so only one durable
		// cell exists without deleting any historical row.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			j.SampleID+"\x1f"+j.WantEnvJSON); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			SELECT id FROM verification_jobs
			 WHERE sample_id=$1 AND reason='matrix' AND want_env IS NOT DISTINCT FROM $2::jsonb
			 ORDER BY id LIMIT 1`, j.SampleID, wantEnv).Scan(&id)
		if err == nil {
			return tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO verification_jobs(sample_id, reason, want_env)
			VALUES($1,'matrix',$2::jsonb) RETURNING id`, j.SampleID, wantEnv).Scan(&id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return id, err
}

// OpenJobs lists claimable jobs. A job that pins want_env.sandboxCapability
// only matches peers reporting that capability; capability "" matches all.
//
// "Claimable" includes a job whose claim has outlived JobLease. ClaimJob
// has always known how to take one of those back, but this query only ever
// offered status='open', so nothing could reach that path: a peer that
// claimed a job and then died — crashed, upgraded, powered off — held it
// for good. 265 jobs sat claimed with zero open behind a queue that
// reported itself empty, and cross-verification stopped entirely without
// anything reporting an error.
func (p *PG) OpenJobs(ctx context.Context, capability, peerID, reason string, limit int) ([]JobRow, error) {
	return p.OpenJobsPage(ctx, capability, peerID, reason, "", limit, 0)
}

func (p *PG) OpenJobsPage(ctx context.Context, capability, peerID, reason, verifierOS string, limit, offset int) ([]JobRow, error) {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	var out []JobRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT id, sample_id, reason, COALESCE(want_env::text,''), status,
			       COALESCE(claimed_by,''), claimed_at, created_at
			FROM verification_jobs j
			WHERE (status='open'
			       OR (status='claimed' AND claimed_at < now() - $3::interval))
			  AND ($5 = '' OR j.reason = $5)
			  AND ($1 = ''
				OR want_env IS NULL
				OR want_env->>'sandboxCapability' IS NULL
				OR want_env->>'sandboxCapability' = $1)
			  -- A peer that already JUDGED this sample cannot cross-verify it.
			  -- Only a receipt that reached a verdict counts: one whose contract
			  -- never ran (resolve or compile died, contract SKIPPED) says nothing
			  -- about the sample and used to lock its peer out forever. See
			  -- ContractWasJudged.
			  AND ($4 = '' OR j.reason <> 'cross' OR NOT EXISTS (
				SELECT 1 FROM receipts r
				 WHERE r.sample_id = j.sample_id AND r.peer_id = $4
				   AND upper(coalesce(r.contract_result,'')) IN ('PASS','FAIL')))
			  -- A job names the platform its sample needs. Without this the queue
			  -- handed a Linux verifier the Windows rows too: the window is twenty
			  -- deep, and whatever it could actually run was whatever was left. The
			  -- one Windows verifier on the network waited behind that. A job that
			  -- names no OS runs anywhere and is never hidden.
			  AND ($7 = ''
				OR want_env IS NULL
				OR want_env->>'os' IS NULL
				OR lower(want_env->>'os') = lower($7))
			ORDER BY created_at, id
			LIMIT $2 OFFSET $6`, capability, limit, JobLease.String(), peerID, reason, offset, verifierOS)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}

// JobsForSample lists every job for a sample regardless of status.
// VersionCoresidence lists the version pairs of one library that a scanner
// saw in a single resolution, counted by distinct project-days.
func (p *PG) VersionCoresidence(ctx context.Context, ecosystem, name string) ([]VersionCoresidence, error) {
	var out []VersionCoresidence
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT lower_version, higher_version,
			       count(*) AS projects,
			       count(*) FILTER (WHERE failing) AS failing
			  FROM version_coresidence
			 WHERE ecosystem = $1 AND name = $2
			 GROUP BY 1, 2
			 ORDER BY failing DESC, projects DESC, lower_version, higher_version`,
			ecosystem, name)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r VersionCoresidence
			if err := rows.Scan(&r.Lower, &r.Higher, &r.Projects, &r.Failing); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	// Rows written before the precedence fix are stored lexicographically
	// ("10.0.0" below "9.0.0"); heal and fold them here rather than
	// migrating, since SQL cannot compare versions.
	return canonicalCoresidencePairs(out), nil
}

// Dependants lists what pulled each version of one library.
func (p *PG) Dependants(ctx context.Context, ecosystem, name string) ([]DependencyEdge, error) {
	var out []DependencyEdge
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT parent_name, parent_version, child_name, child_version, count(*) AS projects
			  FROM dependency_edge
			 WHERE ecosystem = $1 AND child_name = $2
			 GROUP BY 1, 2, 3, 4`, ecosystem, name)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e DependencyEdge
			if err := rows.Scan(&e.ParentName, &e.ParentVersion, &e.ChildName, &e.ChildVersion, &e.Projects); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	sortDependencyEdges(out)
	return out, nil
}

// Dependencies lists what shipped ALONGSIDE each version of one package.
//
// Upgrade a library and its dependencies move under you; the one that moved
// is usually the one that broke the build. Same table as Dependants, read
// from the parent end.
func (p *PG) Dependencies(ctx context.Context, ecosystem, name string) ([]DependencyEdge, error) {
	var out []DependencyEdge
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT parent_name, parent_version, child_name, child_version, count(*) AS projects
			  FROM dependency_edge
			 WHERE ecosystem = $1 AND parent_name = $2
			 GROUP BY 1, 2, 3, 4`, ecosystem, name)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e DependencyEdge
			if err := rows.Scan(&e.ParentName, &e.ParentVersion, &e.ChildName, &e.ChildVersion, &e.Projects); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	sortShippedWith(out)
	return out, nil
}

// StrandedDrafts lists quarantined authoring drafts that have no verification
// left to wait for.
//
// A verifier that cannot resolve dependencies files a SKIPPED receipt, which
// closes the sample's only cross job without measuring anything. Before the
// retry existed nothing queued another, and production accumulated 159 drafts
// in exactly that state — verified by nobody, waiting on nothing, invisible.
// This is how the reconcile finds them.
func (p *PG) StrandedDrafts(ctx context.Context, maxAttempts, limit int) ([]string, error) {
	var out []string
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT s.sample_id
			  FROM samples s
			 WHERE s.status='DRAFT' AND s.quarantined
			   AND NOT EXISTS (SELECT 1 FROM receipts r
			                    WHERE r.sample_id=s.sample_id AND r.contract_result='PASS')
			   AND NOT EXISTS (SELECT 1 FROM verification_jobs j
			                    WHERE j.sample_id=s.sample_id AND j.status IN ('open','claimed'))
			   AND (SELECT count(*) FROM verification_jobs j
			         WHERE j.sample_id=s.sample_id AND j.reason='cross') < $1
			 ORDER BY s.created_at
			 LIMIT $2`, maxAttempts, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) JobsForSample(ctx context.Context, sampleID string) ([]JobRow, error) {
	var out []JobRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT id, sample_id, reason, COALESCE(want_env::text,''), status,
			       COALESCE(claimed_by,''), claimed_at, created_at
			FROM verification_jobs
			WHERE sample_id=$1
			ORDER BY created_at, id`, sampleID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) Job(ctx context.Context, id int64) (JobRow, bool, error) {
	var out JobRow
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var err error
		out, err = scanJob(c.QueryRow(ctx, `
			SELECT id, sample_id, reason, COALESCE(want_env::text,''), status,
			       COALESCE(claimed_by,''), claimed_at, created_at
			FROM verification_jobs WHERE id=$1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			found = true
		}
		return err
	})
	return out, found, err
}

func scanJob(row pgx.Row) (JobRow, error) {
	var j JobRow
	var claimedAt, createdAt *time.Time
	err := row.Scan(&j.ID, &j.SampleID, &j.Reason, &j.WantEnvJSON, &j.Status,
		&j.ClaimedBy, &claimedAt, &createdAt)
	if err != nil {
		return JobRow{}, err
	}
	if claimedAt != nil {
		j.ClaimedAt = *claimedAt
	}
	if createdAt != nil {
		j.CreatedAt = *createdAt
	}
	return j, nil
}

// JobLease is how long a claim holds a job before it returns to the queue.
//
// A claim used to be permanent, and nothing ever called CompleteJob — there
// was no route to it — so every claimed job was stranded for good. Claiming
// needs no authentication, and deliberately so: publishing on this network
// is anonymous and accounts are not the answer (goal.md §8.6). But that
// makes an unbounded claim a way for one stranger to empty the verification
// queue permanently, at no cost. A lease is the version of this that cannot
// be abused: an abandoned claim expires and someone else picks the job up.
const JobLease = 30 * time.Minute

func (p *PG) ClaimJob(ctx context.Context, id int64, peerID string) (bool, error) {
	claimed := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) // no-op after commit
		var sampleID string
		if err := tx.QueryRow(ctx, `SELECT sample_id FROM verification_jobs WHERE id=$1`, id).Scan(&sampleID); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		// Different matrix cells for one sample can race through separate HTTP
		// requests. Serialize only this peer+sample pair so both statements
		// cannot observe "no active claim" and take a job simultaneously.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, sampleID+"\x1f"+peerID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE verification_jobs
			SET status='claimed', claimed_by=$2, claimed_at=now()
			WHERE id=$1
			  AND (status='open'
			       OR (status='claimed' AND claimed_at < now() - $3::interval))
			  AND NOT EXISTS (
			    SELECT 1 FROM verification_jobs other
			     WHERE other.id <> verification_jobs.id
			       AND other.sample_id = verification_jobs.sample_id
			       AND other.status='claimed' AND other.claimed_by=$2
			       AND other.claimed_at >= now() - $3::interval)`,
			id, peerID, JobLease.String())
		if err != nil {
			return err
		}
		claimed = tag.RowsAffected() == 1
		return tx.Commit(ctx)
	})
	return claimed, err
}

// CompleteJobsForSample closes every open or claimed job for a sample.
//
// A receipt arriving IS the completion — that is what the job asked for —
// and wiring it here means a finished job cannot be left behind by a peer
// that never calls anything else.
func (p *PG) CompleteJobsForSample(ctx context.Context, sampleID, peerID string) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		// A cross job asks a SECOND peer to reproduce the result. A receipt
		// from the peer that originated the sample answers nothing — it
		// proves only that the sample still works where it was built — so
		// it must not close the job. It used to: a machine that published a
		// sample and also ran a verifier would claim its own cross job,
		// file its own receipt, and retire the job having cross-verified
		// nothing. The sample then sat at PUBLISHED forever with no open
		// job to explain why.
		_, err := c.Exec(ctx,
			`UPDATE verification_jobs j SET status='done'
			 WHERE j.sample_id=$1 AND j.reason='cross' AND j.status IN ('open','claimed')
			   AND (j.reason <> 'cross' OR $2 = '' OR $2 IS DISTINCT FROM (
			         SELECT r.peer_id FROM receipts r
			          WHERE r.sample_id=$1 ORDER BY r.created_at, r.receipt_id LIMIT 1))`,
			sampleID, peerID)
		return err
	})
}

func (p *PG) CompleteJob(ctx context.Context, id int64) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx,
			`UPDATE verification_jobs SET status='done' WHERE id=$1`, id)
		return err
	})
}

// ------------------------------------------------------------------ peers --

func (p *PG) AnnouncePeer(ctx context.Context, peer PeerRow) error {
	var caps, sampleIDs []byte
	if peer.CapabilitiesJSON != "" {
		caps = []byte(peer.CapabilitiesJSON)
	}
	if peer.SampleIDsJSON != "" {
		sampleIDs = []byte(peer.SampleIDsJSON)
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO peers(peer_id, addr, port, capabilities, sample_ids, announced_at, expires_at)
			VALUES($1,$2,$3,$4,$5,now(),$6)
			ON CONFLICT (peer_id) DO UPDATE SET
				addr = EXCLUDED.addr,
				port = EXCLUDED.port,
				capabilities = EXCLUDED.capabilities,
				sample_ids = EXCLUDED.sample_ids,
				announced_at = now(),
				expires_at = EXCLUDED.expires_at`,
			peer.PeerID, peer.Addr, peer.Port, caps, sampleIDs, peer.ExpiresAt)
		return err
	})
}

func (p *PG) PeersForSample(ctx context.Context, sampleID string) ([]PeerRow, error) {
	var out []PeerRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT peer_id, addr, port, COALESCE(capabilities::text,''),
			       COALESCE(sample_ids::text,''), announced_at, expires_at
			FROM peers
			WHERE expires_at > now() AND sample_ids @> jsonb_build_array($1::text)
			ORDER BY announced_at DESC`, sampleID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pr PeerRow
			var announced, expires *time.Time
			if err := rows.Scan(&pr.PeerID, &pr.Addr, &pr.Port, &pr.CapabilitiesJSON,
				&pr.SampleIDsJSON, &announced, &expires); err != nil {
				return err
			}
			if announced != nil {
				pr.AnnouncedAt = *announced
			}
			if expires != nil {
				pr.ExpiresAt = *expires
			}
			out = append(out, pr)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) ExpirePeers(ctx context.Context, now time.Time) (int64, error) {
	var removed int64
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx, `DELETE FROM peers WHERE expires_at <= $1`, now)
		if err != nil {
			return err
		}
		removed = tag.RowsAffected()
		return nil
	})
	return removed, err
}

// ----------------------------------------------------------------- shards --

func (p *PG) PutShard(ctx context.Context, key, etag, shardJSON string) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO shards(key, etag, json, generated_at)
			VALUES($1,$2,$3,now())
			ON CONFLICT (key) DO UPDATE SET
				etag = EXCLUDED.etag, json = EXCLUDED.json, generated_at = now()`,
			key, etag, []byte(shardJSON))
		return err
	})
}

// ShardKeys lists every stored shard key, ordered so a pass is repeatable.
func (p *PG) ShardKeys(ctx context.Context) ([]string, error) {
	var keys []string
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT key FROM shards ORDER BY key`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			keys = append(keys, k)
		}
		return rows.Err()
	})
	return keys, err
}

// hotShardScanLimit bounds the per-package scan behind HotShardKeys. The
// tail of a long-tail distribution cannot reach the top of the list, so
// reading all of it would only cost time.
const hotShardScanLimit = 5000

// sampleShardWeight makes one verified sample outrank any amount of
// observation volume. Warming is about what a fresh install can ANSWER
// with: ranking purely by observations fills the cache with transitive
// dependencies nobody asks about while the shards that actually carry
// samples fall off the end of the list.
const sampleShardWeight = 1_000_000

// HotShardKeys ranks built shards by the samples they carry first, then by
// the observation volume of the packages they cover. Keys with no shard
// row are dropped: handing a client a key that 404s wastes the one warm-up
// it gets.
func (p *PG) HotShardKeys(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	weight := map[string]int64{}
	built := map[string]bool{}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT purl, SUM(observation_count) AS n
			FROM evidence_agg
			GROUP BY purl
			ORDER BY n DESC
			LIMIT $1`, hotShardScanLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var purl string
			var n int64
			if err := rows.Scan(&purl, &n); err != nil {
				return err
			}
			pu, perr := domain.ParsePURL(purl)
			if perr != nil {
				continue
			}
			weight[pu.Ecosystem+"/"+pu.Name+"/"+pu.Major()] += n
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// Every package a published sample declares, plus every exact version
		// its v2 receipts established, so shards holding answers rank above
		// shards holding only counts. The sample id participates in DISTINCT:
		// repeated cross-verification must not manufacture extra popularity.
		//
		// NOT the quarantined ones. A withdrawn sample carried the same
		// +1,000,000 weight as a live one, so it kept pushing its package
		// to the top of the HOT list -- the list every client warms first.
		// "Hides a sample from every serving read" has to include the read
		// that decides what the whole network downloads.
		samplePURLs := map[[2]string]bool{}
		srows, err := c.Query(ctx, `
			SELECT sample_id, manifest::text FROM samples WHERE NOT quarantined`)
		if err != nil {
			return err
		}
		for srows.Next() {
			var sampleID, manifestJSON string
			if err := srows.Scan(&sampleID, &manifestJSON); err != nil {
				srows.Close()
				return err
			}
			var manifest struct {
				Packages []string `json:"packages"`
			}
			if json.Unmarshal([]byte(manifestJSON), &manifest) != nil {
				continue
			}
			for _, purl := range manifest.Packages {
				samplePURLs[[2]string{sampleID, purl}] = true
			}
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return err
		}

		rrows, err := c.Query(ctx, `
			SELECT s.sample_id, r.receipt::text
			FROM samples s JOIN receipts r ON r.sample_id = s.sample_id
			WHERE NOT s.quarantined
			  AND r.receipt->>'schemaVersion' = '2'
			  AND r.receipt->'stages'->>'resolve' = 'PASS'`)
		if err != nil {
			return err
		}
		for rrows.Next() {
			var sampleID, receiptJSON string
			if err := rrows.Scan(&sampleID, &receiptJSON); err != nil {
				rrows.Close()
				return err
			}
			for _, purl := range resolvedPackageStrings(receiptJSON) {
				samplePURLs[[2]string{sampleID, purl}] = true
			}
		}
		rrows.Close()
		if err := rrows.Err(); err != nil {
			return err
		}
		for samplePURL := range samplePURLs {
			pu, perr := domain.ParsePURL(samplePURL[1])
			if perr != nil {
				continue
			}
			weight[pu.Ecosystem+"/"+pu.Name+"/"+pu.Major()] += sampleShardWeight
		}

		krows, err := c.Query(ctx, `SELECT key FROM shards`)
		if err != nil {
			return err
		}
		defer krows.Close()
		for krows.Next() {
			var k string
			if err := krows.Scan(&k); err != nil {
				return err
			}
			built[k] = true
		}
		return krows.Err()
	})
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(built))
	for k := range built {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if weight[keys[i]] != weight[keys[j]] {
			return weight[keys[i]] > weight[keys[j]]
		}
		return keys[i] < keys[j] // stable output for an unchanged network
	})

	// Never truncate a shard that carries a sample. The limit exists to
	// bound how much observation data a fresh install pulls, not to hide
	// answers: cutting at a fixed count dropped the jinja2 and tenacity
	// shards, so their samples were unreachable from a new install however
	// good they were. Samples are the product; counts are context.
	withSamples := 0
	for _, k := range keys {
		if weight[k] >= sampleShardWeight {
			withSamples++
		}
	}
	if limit < withSamples {
		limit = withSamples
	}
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

func (p *PG) GetShard(ctx context.Context, key string) (string, string, bool, error) {
	var etag, js string
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		err := c.QueryRow(ctx,
			`SELECT etag, json::text FROM shards WHERE key=$1`, key).Scan(&etag, &js)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return etag, js, found, err
}

// GetShardEtag reads the ETag alone: no json::text detoast, so the
// revalidation path costs a primary-key row read rather than the document.
func (p *PG) GetShardEtag(ctx context.Context, key string) (string, bool, error) {
	var etag string
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		err := c.QueryRow(ctx,
			`SELECT etag FROM shards WHERE key=$1`, key).Scan(&etag)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return etag, found, err
}

// ------------------------------------------------------------- identities --

func (p *PG) SaveIdentity(ctx context.Context, login string, githubID int64, tokenHash, apiTokenHash string) error {
	var gh *int64
	if githubID != 0 {
		gh = &githubID
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO identities(login, github_id, display, token_hash, api_token_hash)
			VALUES($1,$2,$1,$3,$4)
			ON CONFLICT (login) DO UPDATE SET
				github_id = EXCLUDED.github_id,
				token_hash = EXCLUDED.token_hash,
				api_token_hash = EXCLUDED.api_token_hash`,
			login, gh, tokenHash, apiTokenHash)
		return err
	})
}

func (p *PG) IdentityByAPIToken(ctx context.Context, apiTokenHash string) (IdentityRow, bool, error) {
	var id IdentityRow
	found := false
	if apiTokenHash == "" { // never match rows with NULL/empty token hashes
		return id, false, nil
	}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var gh *int64
		var created *time.Time
		err := c.QueryRow(ctx, `
			SELECT login, github_id, COALESCE(display,''), COALESCE(token_hash,''),
			       COALESCE(api_token_hash,''), created_at
			FROM identities WHERE api_token_hash=$1`, apiTokenHash,
		).Scan(&id.Login, &gh, &id.Display, &id.TokenHash, &id.APITokenHash, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if gh != nil {
			id.GithubID = *gh
		}
		if created != nil {
			id.CreatedAt = *created
		}
		found = true
		return nil
	})
	return id, found, err
}

// --------------------------------------------------------------- clusters --

func (p *PG) UpsertFailureCluster(ctx context.Context, cl ClusterRow) error {
	var envSummary, hypotheses, versions []byte
	if cl.EnvSummaryJSON != "" {
		envSummary = []byte(cl.EnvSummaryJSON)
	}
	if cl.HypothesesJSON != "" {
		hypotheses = []byte(cl.HypothesesJSON)
	}
	if cl.VersionsJSON != "" {
		versions = []byte(cl.VersionsJSON)
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO failure_clusters(ecosystem, package_name, symbol, stage, error_fp,
				error_code, observation_count, env_summary, hypotheses,
				regression_candidate, versions)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (ecosystem, package_name, symbol, stage, error_fp) DO UPDATE SET
				error_code = EXCLUDED.error_code,
				observation_count = EXCLUDED.observation_count,
				env_summary = EXCLUDED.env_summary,
				hypotheses = EXCLUDED.hypotheses,
				regression_candidate = EXCLUDED.regression_candidate,
				versions = EXCLUDED.versions,
				last_seen = now()`,
			cl.Ecosystem, cl.PackageName, cl.Symbol, cl.Stage, cl.ErrorFingerprint,
			cl.ErrorCode, cl.ObservationCount, envSummary, hypotheses,
			cl.RegressionCandidate, versions)
		return err
	})
}

func (p *PG) ListFailureClusters(ctx context.Context, packageName string) ([]ClusterRow, error) {
	var out []ClusterRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT id, COALESCE(ecosystem,''), COALESCE(package_name,''),
			       COALESCE(symbol,''), COALESCE(stage,''), COALESCE(error_fp,''),
			       COALESCE(error_code,''), COALESCE(observation_count,0),
			       COALESCE(env_summary::text,''), COALESCE(hypotheses::text,''),
			       COALESCE(regression_candidate,false), COALESCE(versions::text,''),
			       first_seen, last_seen
			FROM failure_clusters
			WHERE package_name=$1
			ORDER BY observation_count DESC, id`, packageName)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cl ClusterRow
			var first, last *time.Time
			if err := rows.Scan(&cl.ID, &cl.Ecosystem, &cl.PackageName, &cl.Symbol,
				&cl.Stage, &cl.ErrorFingerprint, &cl.ErrorCode, &cl.ObservationCount,
				&cl.EnvSummaryJSON, &cl.HypothesesJSON, &cl.RegressionCandidate,
				&cl.VersionsJSON, &first, &last); err != nil {
				return err
			}
			if first != nil {
				cl.FirstSeen = *first
			}
			if last != nil {
				cl.LastSeen = *last
			}
			out = append(out, cl)
		}
		return rows.Err()
	})
	return out, err
}

// ------------------------------------------------------------------ stats --

func (p *PG) SetStatsDaily(ctx context.Context, day string, statsJSON string) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO stats_daily(day, stats)
			VALUES($1::date, $2)
			ON CONFLICT (day) DO UPDATE SET stats = EXCLUDED.stats`,
			day, []byte(statsJSON))
		return err
	})
}

func (p *PG) GetLatestStats(ctx context.Context) (string, bool, error) {
	var js string
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		err := c.QueryRow(ctx,
			`SELECT stats::text FROM stats_daily ORDER BY day DESC LIMIT 1`).Scan(&js)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return js, found, err
}

// --------------------------------------------------------------- activity --

// RecordActivity persists only epoch-scoped 128-bit buckets. owner is
// monotonic so an authenticated admin visit retroactively excludes a bucket
// even when an older non-owner observation arrives later from the queue.
func (p *PG) RecordActivity(ctx context.Context, buckets []activity.Bucket) error {
	if len(buckets) == 0 {
		return nil
	}
	for _, bucket := range buckets {
		if !validActivityEpoch(bucket.Kind, bucket.Epoch) || bucket.SeenAt.IsZero() || !activityEpochMatchesSeenAt(bucket) {
			return errors.New("serverstore: invalid activity bucket")
		}
	}
	ordered := append([]activity.Bucket(nil), buckets...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Kind != ordered[j].Kind {
			return ordered[i].Kind < ordered[j].Kind
		}
		if ordered[i].Epoch != ordered[j].Epoch {
			return ordered[i].Epoch < ordered[j].Epoch
		}
		return bytes.Compare(ordered[i].Value[:], ordered[j].Value[:]) < 0
	})
	return p.withConn(ctx, func(c *pgx.Conn) error {
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			lastErr = recordActivityAttempt(ctx, c, ordered)
			if lastErr == nil || !retryableActivityTransaction(lastErr) {
				return lastErr
			}
			if attempt < 2 {
				timer := time.NewTimer(time.Duration(attempt+1) * 5 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		return fmt.Errorf("serverstore: activity transaction failed after 3 attempts: %w", lastErr)
	})
}

func recordActivityAttempt(ctx context.Context, c *pgx.Conn, buckets []activity.Bucket) error {
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var batch pgx.Batch
	for _, bucket := range buckets {
		batch.Queue(`
				INSERT INTO activity_buckets(kind, epoch, bucket, owner, first_seen, last_seen)
				SELECT $1, $2, $3, $4, $5, $5
				WHERE ($1='day' AND $2 BETWEEN
					to_char((now() AT TIME ZONE 'UTC')::date - 34, 'YYYY-MM-DD') AND
					to_char((now() AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD'))
				   OR ($1='month' AND $2 BETWEEN
					to_char(date_trunc('month', now() AT TIME ZONE 'UTC') - interval '12 months', 'YYYY-MM') AND
					to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM'))
				ON CONFLICT (kind, epoch, bucket) DO UPDATE SET
					owner = activity_buckets.owner OR EXCLUDED.owner,
					first_seen = LEAST(activity_buckets.first_seen, EXCLUDED.first_seen),
					last_seen = GREATEST(activity_buckets.last_seen, EXCLUDED.last_seen)`,
			bucket.Kind, bucket.Epoch, bucket.Value[:], bucket.Owner, bucket.SeenAt.UTC())
	}
	results := tx.SendBatch(ctx, &batch)
	for range buckets {
		tag, err := results.Exec()
		if err != nil {
			_ = results.Close()
			return err
		}
		if tag.RowsAffected() != 1 {
			_ = results.Close()
			return errors.New("serverstore: activity epoch is outside the retention window")
		}
	}
	if err := results.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func retryableActivityTransaction(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}

func validActivityEpoch(kind, epoch string) bool {
	var layout string
	switch kind {
	case activity.KindDay:
		layout = "2006-01-02"
	case activity.KindMonth:
		layout = "2006-01"
	default:
		return false
	}
	parsed, err := time.Parse(layout, epoch)
	return err == nil && parsed.Format(layout) == epoch
}

func activityEpochMatchesSeenAt(bucket activity.Bucket) bool {
	switch bucket.Kind {
	case activity.KindDay:
		return bucket.Epoch == bucket.SeenAt.UTC().Format("2006-01-02")
	case activity.KindMonth:
		return bucket.Epoch == bucket.SeenAt.UTC().Format("2006-01")
	default:
		return false
	}
}

func (p *PG) ActivityCounts(ctx context.Context, dayEpoch, monthEpoch string) (activity.Counts, error) {
	var out activity.Counts
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE kind='day' AND epoch=$1 AND NOT owner),
				COUNT(*) FILTER (WHERE kind='month' AND epoch=$2 AND NOT owner),
				COUNT(*) FILTER (WHERE kind='day' AND epoch=$1 AND owner),
				COUNT(*) FILTER (WHERE kind='month' AND epoch=$2 AND owner),
				EXISTS(SELECT 1 FROM activity_buckets WHERE kind='day' AND epoch=$1 AND NOT owner),
				EXISTS(SELECT 1 FROM activity_buckets WHERE kind='month' AND epoch=$2 AND NOT owner)
			FROM activity_buckets
			WHERE (kind='day' AND epoch=$1) OR (kind='month' AND epoch=$2)`,
			dayEpoch, monthEpoch).Scan(&out.ExternalDAU, &out.ExternalMAU, &out.OwnerDAU, &out.OwnerMAU, &out.DaySeen, &out.MonthSeen)
	})
	return out, err
}

// ActivityDaily backs the daily chart. A bounded daily health marker makes a
// healthy zero distinguishable from both a collection gap and a day before
// collection. Bucket rows remain separate so traffic proves collection even
// for an epoch created before health markers were introduced.
func (p *PG) ActivityDaily(ctx context.Context, fromEpoch, toEpoch string) (activity.DailyRaw, error) {
	if !validActivityEpoch(activity.KindDay, fromEpoch) || !validActivityEpoch(activity.KindDay, toEpoch) {
		return activity.DailyRaw{}, errors.New("serverstore: invalid activity day epoch range")
	}
	var out activity.DailyRaw
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			WITH bucket_days AS (
				SELECT epoch,
				       COUNT(*) FILTER (WHERE NOT owner) AS external,
				       COUNT(*) FILTER (WHERE owner) AS owner_excluded,
				       COUNT(*) AS total
				  FROM activity_buckets
				 WHERE kind='day' AND epoch BETWEEN $1 AND $2
				 GROUP BY epoch
			), epochs AS (
				SELECT epoch FROM activity_health WHERE epoch BETWEEN $1 AND $2
				UNION
				SELECT epoch FROM bucket_days
			)
			SELECT e.epoch,
			       COALESCE(b.external, 0),
			       COALESCE(b.owner_excluded, 0),
			       COALESCE(b.total, 0),
			       EXISTS(SELECT 1 FROM activity_health h WHERE h.epoch=e.epoch)
			  FROM epochs e
			  LEFT JOIN bucket_days b ON b.epoch=e.epoch
			 ORDER BY e.epoch`, fromEpoch, toEpoch)
		if err != nil {
			return err
		}
		// A fresh slice per attempt: withConn may re-run this closure on a new
		// connection, and a retry must not append to the previous result.
		var days []activity.DayCount
		for rows.Next() {
			var d activity.DayCount
			if err := rows.Scan(&d.Epoch, &d.Count, &d.OwnerExcluded, &d.Rows, &d.Healthy); err != nil {
				rows.Close()
				return err
			}
			days = append(days, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		out.Days = days
		var oldest *string
		if err := c.QueryRow(ctx, `
			SELECT MIN(epoch) FROM (
				SELECT epoch FROM activity_health
				UNION
				SELECT epoch FROM activity_buckets WHERE kind='day'
			) retained_days`).Scan(&oldest); err != nil {
			return err
		}
		if oldest != nil {
			out.OldestEpoch = *oldest
		}
		return nil
	})
	if err != nil {
		return activity.DailyRaw{}, err
	}
	return out, nil
}

// MarkActivityHealthy records only the current UTC day's explicit collection
// health marker. The tracker calls it at startup and at each UTC rollover,
// independently of collector-key readiness; one call never backfills history.
func (p *PG) MarkActivityHealthy(ctx context.Context, now time.Time) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO activity_health(epoch, checked_at)
			VALUES ($1, $2)
			ON CONFLICT (epoch) DO UPDATE SET
				checked_at = GREATEST(activity_health.checked_at, EXCLUDED.checked_at)`,
			now.UTC().Format("2006-01-02"), now.UTC())
		return err
	})
}

// PruneActivity bounds linkable state to exactly 35 daily and 13 monthly
// epochs including the current UTC day/month. It remains on the independent
// six-hour maintenance cadence. Both tails are deleted, including health
// markers, so a skewed future row cannot evade a lower-bound-only pass.
func (p *PG) PruneActivity(ctx context.Context, now time.Time) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var batch pgx.Batch
		batch.Queue(`
			DELETE FROM activity_buckets
			WHERE (kind='day' AND epoch < to_char(($1::timestamptz AT TIME ZONE 'UTC')::date - 34, 'YYYY-MM-DD'))
			   OR (kind='day' AND epoch > to_char(($1::timestamptz AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD'))
			   OR (kind='month' AND epoch < to_char(date_trunc('month', $1::timestamptz AT TIME ZONE 'UTC') - interval '12 months', 'YYYY-MM'))
			   OR (kind='month' AND epoch > to_char($1::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM'))`, now.UTC())
		batch.Queue(`
			DELETE FROM activity_health
			WHERE epoch < to_char(($1::timestamptz AT TIME ZONE 'UTC')::date - 34, 'YYYY-MM-DD')
			   OR epoch > to_char(($1::timestamptz AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD')`, now.UTC())
		results := tx.SendBatch(ctx, &batch)
		for i := 0; i < 2; i++ {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return err
			}
		}
		if err := results.Close(); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

// NetworkCounts computes the raw /v1/stats numbers in one round trip.
//
// Peers counts the distinct anonymous peer buckets that contributed
// evidence in the current epoch — the people actually using the network
// today. It deliberately does NOT count rows in the peers table: that is
// the P2P blob tracker, which a node only joins by opting into
// peerListen, so it read 0 while evidence was arriving every minute.
// Peer identities rotate daily by design (goal.md §8.6), so one day is
// the longest window that can be counted without inflating one user into
// many.
func (p *PG) NetworkCounts(ctx context.Context, now time.Time) (NetworkCounts, error) {
	var c NetworkCounts
	epoch := now.UTC().Format("2006-01-02")
	monthStart := now.UTC().Format("2006-01") + "-01"
	err := p.withConn(ctx, func(conn *pgx.Conn) error {
		return conn.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(DISTINCT bucket) FROM evidence_dedup
					WHERE bucket_kind = 'peer' AND epoch = $2),
				-- Project buckets rotate monthly, so distinct buckets within
				-- the month are distinct projects — the one participation
				-- number here that does not reset every midnight.
				(SELECT COUNT(DISTINCT bucket) FROM evidence_dedup
					WHERE bucket_kind = 'project' AND epoch >= $3),
				-- Only packages the registry check CONFIRMED public. The
				-- packages table doubles as the publicness cache, so a row
				-- exists for every purl anyone has ever mentioned —
				-- including the ones checked and found NOT to exist. An
				-- anonymous POST of twenty invented purls was rejected for
				-- ingest and still added twenty rows, so the "Packages"
				-- figure on the front page could be inflated by a stranger
				-- naming things that do not exist.
				(SELECT COUNT(*) FROM (SELECT DISTINCT ecosystem, name FROM packages
					WHERE publicness = 'PUBLIC') t),
				(SELECT COUNT(DISTINCT symbol) FROM evidence_agg WHERE symbol <> ''),
				-- Package-level rows only. One build writes a package-level
				-- observation AND one per detected symbol, so summing every
				-- row counts the same build again for each symbol found in
				-- it -- 38% of this figure was that.
				--
				-- And runs only. USED records that a package was PRESENT,
				-- not that anything was exercised: it has no failing form,
				-- and it carried 8,686 of the 42,808 package-level events,
				-- so counting it made "observations" partly a head count of
				-- installed dependencies.
				(SELECT COALESCE(SUM(observation_count),0)
				   FROM evidence_agg WHERE symbol = '' AND stage LIKE 'PROJECT%'),
				-- "Verified" means a contract actually passed in a sandbox,
				-- which is a receipt fact. Counting only CROSS_PASS+ reported
				-- zero for every contract-verified sample still waiting for a
				-- second peer, which reads as "nothing here works".
				(SELECT COUNT(DISTINCT s.sample_id) FROM samples s
					WHERE s.status IN ('CROSS_PASS','MATRIX_PASS','STABLE')
					   OR EXISTS (SELECT 1 FROM receipts r
					              WHERE r.sample_id = s.sample_id AND r.contract_result = 'PASS')),
				(SELECT COUNT(*) FROM peers WHERE expires_at > $1)`, now, epoch, monthStart,
		).Scan(&c.Peers, &c.ProjectsMonth, &c.Packages, &c.Symbols, &c.Observations,
			&c.VerifiedSamples, &c.ServingPeers)
	})
	return c, err
}

// ------------------------------------------------------------------- pool --

// connPool is a minimal *pgx.Conn pool: a semaphore bounds total open
// connections and a buffered channel holds idle ones.
type connPool struct {
	cfg    *pgx.ConnConfig
	idle   chan *pgx.Conn
	sem    chan struct{} // one token per open connection
	closed atomic.Bool
}

func newConnPool(cfg *pgx.ConnConfig, max int) *connPool {
	return &connPool{
		cfg:  cfg,
		idle: make(chan *pgx.Conn, max),
		sem:  make(chan struct{}, max),
	}
}

func (p *connPool) acquire(ctx context.Context) (*pgx.Conn, error) {
	if p.closed.Load() {
		return nil, errors.New("serverstore: store is closed")
	}
	// Fast path: an idle connection.
	select {
	case c := <-p.idle:
		return p.ensureAlive(ctx, c)
	default:
	}
	// Open a new connection if under the cap, else wait for an idle one.
	select {
	case c := <-p.idle:
		return p.ensureAlive(ctx, c)
	case p.sem <- struct{}{}:
		c, err := pgx.ConnectConfig(ctx, p.cfg)
		if err != nil {
			<-p.sem
			return nil, fmt.Errorf("serverstore: connect: %w", err)
		}
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ensureAlive replaces a dead idle connection, keeping its sem token.
func (p *connPool) ensureAlive(ctx context.Context, c *pgx.Conn) (*pgx.Conn, error) {
	if !c.IsClosed() {
		return c, nil
	}
	fresh, err := pgx.ConnectConfig(ctx, p.cfg)
	if err != nil {
		<-p.sem
		return nil, fmt.Errorf("serverstore: reconnect: %w", err)
	}
	return fresh, nil
}

func (p *connPool) release(c *pgx.Conn) {
	if c == nil {
		return
	}
	if p.closed.Load() || c.IsClosed() {
		_ = c.Close(context.Background())
		<-p.sem
		return
	}
	p.idle <- c // never blocks: cap(idle) == cap(sem)
}

func (p *connPool) close() {
	if p.closed.Swap(true) {
		return
	}
	for {
		select {
		case c := <-p.idle:
			_ = c.Close(context.Background())
			<-p.sem
		default:
			return
		}
	}
}

// ------------------------------------------------------------- wanted --

// WantedRow is one "asked for, not answered" row.
type WantedRow struct {
	Ecosystem string
	Name      string
	Version   string
	Symbol    string
	Asks      int64
	// Kind and Score are private authoring-queue metadata. Public Wanted
	// responses keep using Asks and never serialize these fields directly.
	Kind  string
	Score int64
	// TargetOS is the OS recorded by the evidence that produced a private
	// authoring candidate. Public Wanted rows leave it empty because their
	// reports do not currently carry an execution environment.
	TargetOS  string
	FirstSeen time.Time
	LastSeen  time.Time
	// HasPage is retained on the internal row for wire compatibility. Every
	// supported Wanted coordinate now has an honest request-only page, so it
	// is always true and must not trigger expensive snapshot/sample scans.
	HasPage bool
}

// WantedSubmission keeps one rotating reporter bucket attached to the rows
// it asked for. Batch ingest stores several submissions atomically while the
// dedup key remains per reporter, epoch, package version and symbol.
type WantedSubmission struct {
	Epoch  string
	AnonID string
	Rows   []WantedRow
}

// RecordWanted counts one anonymous report that the network had no answer
// for this package (and symbol, when the caller named one).
//
// Counted per (reporter, epoch), not per request: one machine asking the
// same thing all afternoon is one data point. Counting keystrokes would let
// a single caller manufacture the ranking, which is the whole value of it.
func (p *PG) RecordWanted(ctx context.Context, epoch, anonID string, rows []WantedRow) error {
	return p.RecordWantedBatch(ctx, []WantedSubmission{{Epoch: epoch, AnonID: anonID, Rows: rows}})
}

// RecordWantedBatch applies a wire batch in one database transaction. A
// transport retry is safe because every row first passes through the rotating
// dedup key, and a storage failure cannot leave half the envelope committed.
func (p *PG) RecordWantedBatch(ctx context.Context, reports []WantedSubmission) error {
	if len(reports) == 0 {
		return nil
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		for _, report := range reports {
			for _, r := range report.Rows {
				tag, err := tx.Exec(ctx, `
				INSERT INTO wanted_dedup(ecosystem, name, version, symbol, target_os, epoch, anon_id)
				VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
					r.Ecosystem, r.Name, r.Version, r.Symbol, r.TargetOS, report.Epoch, report.AnonID)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					continue // this reporter already counted today
				}
				if _, err := tx.Exec(ctx, `
				INSERT INTO wanted(ecosystem, name, version, symbol, target_os, asks, first_seen, last_seen)
				VALUES($1,$2,$3,$4,$5,1,now(),now())
				ON CONFLICT (ecosystem, name, version, symbol, target_os) DO UPDATE
				  SET asks = wanted.asks + 1, last_seen = now()`,
					r.Ecosystem, r.Name, r.Version, r.Symbol, r.TargetOS); err != nil {
					return err
				}
			}
		}
		return tx.Commit(ctx)
	})
}

// TopWanted lists the most-asked unanswered package versions and symbols,
// most wanted first. A row closes only when a live sample carries the exact
// canonical PURL and, when requested, the exact symbol. A different release
// or a different API is still an unanswered request.
func (p *PG) TopWanted(ctx context.Context, limit int) ([]WantedRow, error) {
	rows, _, err := p.listWanted(ctx, "", 0, limit, "", "")
	return rows, err
}

func (p *PG) ListWanted(ctx context.Context, query string, offset, limit int) ([]WantedRow, int, error) {
	return p.listWanted(ctx, query, offset, limit, "", "")
}

func (p *PG) WantedForPackage(ctx context.Context, ecosystem, name string) ([]WantedRow, error) {
	rows, _, err := p.listWanted(ctx, "", 0, 100, ecosystem, name)
	return rows, err
}

func (p *PG) listWanted(ctx context.Context, query string, offset, limit int, ecosystem, name string) ([]WantedRow, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	words := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	var out []WantedRow
	var total int64
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			WITH unanswered AS (
				SELECT w.ecosystem, w.name, w.version, w.symbol, w.target_os,
				       w.asks, w.first_seen, w.last_seen,
				       TRUE AS has_page
				  FROM wanted w
				 WHERE ($3 = '' OR (w.ecosystem = $3 AND w.name = $4))
				   AND NOT EXISTS (
			       SELECT 1 FROM samples s
			        WHERE NOT s.quarantined
			          AND EXISTS (
			              SELECT 1 FROM jsonb_array_elements_text(s.manifest->'packages') AS pkg
			               WHERE strpos(pkg, 'pkg:' || w.ecosystem || '/' || w.name || '@') = 1
			                  OR strpos(pkg, 'pkg:' || w.ecosystem || '/' ||
			                     CASE WHEN left(w.name, 1) = '@'
			                          THEN '%40' || substring(w.name from 2)
			                          ELSE w.name END || '@') = 1)
			          AND EXISTS (
			              SELECT 1 FROM receipts answer_receipt
			               WHERE answer_receipt.sample_id = s.sample_id
			                 AND answer_receipt.contract_result = 'PASS'
			                 -- A row that names a platform is answered only by
			                 -- a proof from that platform. Any-pass closure
			                 -- would delete the ask before the platform it was
			                 -- about had been measured at all.
			                 AND (w.target_os = ''
			                      OR LOWER(COALESCE(answer_receipt.receipt->'environment'->>'os','')) = w.target_os)
			                 AND (
			                     -- Pre-version Wanted rows cannot recover the
			                     -- release that was originally requested. Keep
			                     -- the legacy policy honest and broad: any
			                     -- contract pass for the same package answers
			                     -- that unversioned historical row.
			                     w.version = ''
			                     OR (
			                         -- A versioned request is answered only by
			                         -- the signed v2 resolver claim for the
			                         -- release that actually ran. The manifest
			                         -- version is author input and may differ in
			                         -- a matrix verification.
			                         answer_receipt.receipt->>'schemaVersion' = '2'
			                         AND answer_receipt.receipt->'stages'->>'resolve' = 'PASS'
			                         AND COALESCE(answer_receipt.receipt->'resolvedPackages', '[]'::jsonb) ?
			                             ('pkg:' || w.ecosystem || '/' ||
			                              CASE WHEN left(w.name, 1) = '@'
			                                   THEN '%40' || substring(w.name from 2)
			                                   ELSE w.name END || '@' || w.version)
			                     )))
			          AND (w.symbol = '' OR COALESCE(s.manifest->'symbols', '[]'::jsonb) ? w.symbol))
			   AND NOT EXISTS (
			       SELECT 1 FROM unnest($5::text[]) AS wanted_words(word)
			        WHERE strpos(lower(concat_ws(' ', w.ecosystem, w.name, w.version, w.symbol)), word) = 0
			   )
			), counted AS (
				SELECT count(*) AS total FROM unanswered
			), page_rows AS (
				SELECT * FROM unanswered
				 ORDER BY asks DESC, last_seen DESC, ecosystem, name, version, symbol
				 LIMIT $1 OFFSET $2
			)
			SELECT COALESCE(p.ecosystem, ''), COALESCE(p.name, ''),
			       COALESCE(p.version, ''), COALESCE(p.symbol, ''),
			       COALESCE(p.target_os, ''), COALESCE(p.asks, 0),
			       COALESCE(p.first_seen, 'epoch'::timestamptz),
			       COALESCE(p.last_seen, 'epoch'::timestamptz),
			       COALESCE(p.has_page, FALSE), counted.total,
			       p.ecosystem IS NOT NULL AS present
			  FROM counted
			  LEFT JOIN page_rows p ON TRUE
			 ORDER BY p.asks DESC NULLS LAST, p.last_seen DESC NULLS LAST,
			          p.ecosystem, p.name, p.version, p.symbol`,
			limit, offset, ecosystem, name, words)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r WantedRow
			var present bool
			if err := rows.Scan(&r.Ecosystem, &r.Name, &r.Version, &r.Symbol, &r.TargetOS, &r.Asks,
				&r.FirstSeen, &r.LastSeen, &r.HasPage, &total, &present); err != nil {
				return err
			}
			if present {
				out = append(out, r)
			}
		}
		return rows.Err()
	})
	return out, int(total), err
}

// ---------------------------------------------------------- adoptions --

// AdoptionRow is one report that an agent applied a sample.
// SearchHitRow is one recorded search that found something.
//
// Counts only, by construction: the query, the packages, the symbols and the
// environment stay on the caller's machine. What crosses is that a search
// happened, how many results it was handed, and an opaque id that lets the
// adoption which may follow find the search it came from.
type SearchHitRow struct {
	Grade        string
	ResultsShown int
	SampleID     string
	OfferID      string
	Epoch        string
	AnonID       string
}

type AdoptionRow struct {
	SampleID  string
	Applied   bool
	BuildPass *bool // nil = the reporter ran no build
	Epoch     string
	AnonID    string
}

// AdoptionCounts summarises adoption reports for the stats rollup.
//
// The three numbers are kept apart on purpose. Applied says the answer was
// used; BuildPass and BuildFail say whether it then worked. Folding an
// unknown build into either bucket would turn "we did not measure" into a
// claim, which is the failure mode this project cares most about.
type AdoptionCounts struct {
	Reports   int64
	Applied   int64
	BuildPass int64
	BuildFail int64
}

// RecordAdoption stores one adoption report, counting a reporter once per
// sample per epoch. A repeat within the same epoch updates the outcome —
// an agent that reports "applied" and later reports the build result is
// telling us more about the same event, not a second event.
// RecordSearchHit counts one search that found something.
//
// One reporter counts once per offer per day, mirroring adoptions: an agent
// that retries a search all afternoon is one hit, not an afternoon of demand.
// A later report for the same key updates the counts rather than adding a row.
func (p *PG) RecordSearchHit(ctx context.Context, r SearchHitRow) error {
	dedup := r.OfferID
	if dedup == "" {
		dedup = r.SampleID
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO search_hits(grade, results_shown, sample_id, offer_id,
				epoch, anon_id, dedup_key)
			VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7)
			ON CONFLICT (epoch, anon_id, dedup_key) DO UPDATE SET
				grade = EXCLUDED.grade,
				results_shown = EXCLUDED.results_shown,
				sample_id = EXCLUDED.sample_id`,
			r.Grade, r.ResultsShown, r.SampleID, r.OfferID, r.Epoch, r.AnonID, dedup)
		return err
	})
}

func (p *PG) RecordAdoption(ctx context.Context, r AdoptionRow) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO adoptions(sample_id, applied, build_pass, epoch, anon_id)
			VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (sample_id, epoch, anon_id) DO UPDATE SET
				applied = EXCLUDED.applied,
				build_pass = COALESCE(EXCLUDED.build_pass, adoptions.build_pass)`,
			r.SampleID, r.Applied, r.BuildPass, r.Epoch, r.AnonID)
		return err
	})
}

// AdoptionSummary counts adoption reports across the whole network.
func (p *PG) AdoptionSummary(ctx context.Context) (AdoptionCounts, error) {
	var out AdoptionCounts
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `
			SELECT COUNT(*),
			       COUNT(*) FILTER (WHERE applied),
			       COUNT(*) FILTER (WHERE build_pass IS TRUE),
			       COUNT(*) FILTER (WHERE build_pass IS FALSE)
			  FROM adoptions`).Scan(&out.Reports, &out.Applied, &out.BuildPass, &out.BuildFail)
	})
	return out, err
}
