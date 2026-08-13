package serverstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

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
			(purl, symbol, symbol_confidence, env_hash, env_json, stage, result, error_fp, error_code)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (purl, symbol, env_hash, stage, result, error_fp) DO UPDATE SET
			last_seen = now(),
			symbol_confidence = EXCLUDED.symbol_confidence,
			error_code = CASE WHEN evidence_agg.error_code = ''
				THEN EXCLUDED.error_code ELSE evidence_agg.error_code END
		RETURNING id`,
		canonical, b.Symbol, confidence, env.Hash(), []byte(envJSON),
		string(b.Stage), string(b.Result), b.ErrorFingerprint, b.ErrorCode,
	).Scan(&aggID); err != nil {
		return err
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
			unique_peer_buckets = (SELECT COUNT(DISTINCT bucket) FROM evidence_dedup
				WHERE agg_id = $1 AND bucket_kind = 'peer'),
			unique_project_buckets = (SELECT COUNT(DISTINCT bucket) FROM evidence_dedup
				WHERE agg_id = $1 AND bucket_kind = 'project'),
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

func (p *PG) ListSnapshotTargets(ctx context.Context) ([]SnapshotTarget, error) {
	var out []SnapshotTarget
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx,
			`SELECT DISTINCT purl, symbol FROM evidence_agg ORDER BY purl, symbol`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t SnapshotTarget
			if err := rows.Scan(&t.PURL, &t.Symbol); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// ChangedSince implements the incremental-rebuild query. Both halves are
// index-friendly single scans; on an idle network they return nothing and
// the aggregation pass does no work at all.
func (p *PG) ChangedSince(ctx context.Context, since time.Time) (Changes, error) {
	var c Changes
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
				SELECT jsonb_array_elements_text(manifest->'packages') AS pkg
				FROM samples WHERE created_at > $1
				UNION
				SELECT jsonb_array_elements_text(s.manifest->'packages') AS pkg
				FROM samples s JOIN receipts r ON r.sample_id = s.sample_id
				WHERE r.created_at > $1
			) t`, since)
		if err != nil {
			return err
		}
		defer prows.Close()
		for prows.Next() {
			var purl string
			if err := prows.Scan(&purl); err != nil {
				return err
			}
			c.SamplePURLs = append(c.SamplePURLs, purl)
		}
		return prows.Err()
	})
	return c, err
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
				license, size_bytes, hot_score)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (sample_id) DO UPDATE SET
				manifest = EXCLUDED.manifest,
				status = EXCLUDED.status,
				hot_score = EXCLUDED.hot_score`,
			s.SampleID, caseID, []byte(s.ManifestJSON), s.Status, s.OriginSeeder,
			s.License, s.SizeBytes, s.HotScore)
		return err
	})
}

const sampleCols = `sample_id, COALESCE(case_id,''), manifest::text, status,
	COALESCE(origin_seeder,''), license, size_bytes, hot_score, created_at`

func scanSample(row pgx.Row) (SampleRow, error) {
	var s SampleRow
	var created *time.Time
	err := row.Scan(&s.SampleID, &s.CaseID, &s.ManifestJSON, &s.Status,
		&s.OriginSeeder, &s.License, &s.SizeBytes, &s.HotScore, &created)
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

func (p *PG) ListSamples(ctx context.Context, limit int) ([]SampleRow, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []SampleRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT `+sampleCols+` FROM samples
			ORDER BY created_at DESC, sample_id LIMIT $1`, limit)
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
			`UPDATE samples SET status=$2 WHERE sample_id=$1`, sampleID, status)
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
		return c.QueryRow(ctx, `
			INSERT INTO verification_jobs(sample_id, reason, want_env)
			VALUES($1,$2,$3) RETURNING id`,
			j.SampleID, j.Reason, wantEnv).Scan(&id)
	})
	return id, err
}

// OpenJobs lists claimable jobs. A job that pins want_env.sandboxCapability
// only matches peers reporting that capability; capability "" matches all.
func (p *PG) OpenJobs(ctx context.Context, capability string, limit int) ([]JobRow, error) {
	if limit <= 0 {
		limit = 10
	}
	var out []JobRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT id, sample_id, reason, COALESCE(want_env::text,''), status,
			       COALESCE(claimed_by,''), claimed_at, created_at
			FROM verification_jobs
			WHERE status='open' AND ($1 = ''
				OR want_env IS NULL
				OR want_env->>'sandboxCapability' IS NULL
				OR want_env->>'sandboxCapability' = $1)
			ORDER BY created_at, id
			LIMIT $2`, capability, limit)
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

func (p *PG) ClaimJob(ctx context.Context, id int64, peerID string) (bool, error) {
	claimed := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx, `
			UPDATE verification_jobs
			SET status='claimed', claimed_by=$2, claimed_at=now()
			WHERE id=$1 AND status='open'`, id, peerID)
		if err != nil {
			return err
		}
		claimed = tag.RowsAffected() == 1
		return nil
	})
	return claimed, err
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

		// Every package a published sample declares, so shards holding
		// answers rank above shards holding only counts.
		srows, err := c.Query(ctx, `
			SELECT jsonb_array_elements_text(manifest->'packages') FROM samples`)
		if err != nil {
			return err
		}
		for srows.Next() {
			var purl string
			if err := srows.Scan(&purl); err != nil {
				srows.Close()
				return err
			}
			pu, perr := domain.ParsePURL(purl)
			if perr != nil {
				continue
			}
			weight[pu.Ecosystem+"/"+pu.Name+"/"+pu.Major()] += sampleShardWeight
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return err
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
				(SELECT COUNT(*) FROM (SELECT DISTINCT ecosystem, name FROM packages) t),
				(SELECT COUNT(DISTINCT symbol) FROM evidence_agg WHERE symbol <> ''),
				(SELECT COALESCE(SUM(observation_count),0) FROM evidence_agg),
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
