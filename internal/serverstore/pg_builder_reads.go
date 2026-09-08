package serverstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// BuilderPackage selects every version of a dirty package. Receipt regressions
// and clusters cross major boundaries, so a major alone is not a safe bound.
type BuilderPackage struct{ Ecosystem, Name string }

// BuilderReadMetrics counts compact projection rows and the JSON constructed
// from their returned columns, not wire bytes or PostgreSQL heap/index IO.
type BuilderReadMetrics struct{ Rows, Bytes int64 }

func builderCoords(packages []BuilderPackage) []string {
	seen := map[string]bool{}
	for _, p := range packages {
		seen[(domain.PURL{Ecosystem: strings.ToLower(p.Ecosystem), Name: strings.ToLower(p.Name)}).String()] = true
	}
	out := make([]string, 0, len(seen))
	for coord := range seen {
		out = append(out, coord)
	}
	sort.Strings(out)
	return out
}

// The readiness check and selection see the SAME MVCC snapshot. A legacy
// writer must never create an unprojected row between the check and the read.
// Normal writers maintain both atomically; rollback/direct SQL writes fail
// closed until Migrate repairs their indexed stale projections.
func (p *PG) withBuilderRead(ctx context.Context, read func(pgx.Tx) error) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err := checkBuilderProjections(ctx, tx); err != nil {
			return err
		}
		if err := read(tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

// BuilderChangesSince uses timestamp indexes and compact projections. Symbol
// attribution is global: changing/quarantining the narrowest claim also dirties
// the packages of its competitors, including a different ecosystem.
func (p *PG) BuilderChangesSince(ctx context.Context, since time.Time) (Changes, error) {
	var out Changes
	seen := map[string]bool{}
	err := p.withBuilderRead(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, builderChangesEvidenceSQL, since)
		if err != nil {
			return err
		}
		for rows.Next() {
			var t SnapshotTarget
			if err := rows.Scan(&t.PURL, &t.Symbol); err != nil {
				rows.Close()
				return err
			}
			out.Targets = append(out.Targets, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, builderChangesSamplesSQL, since)
		if err != nil {
			return err
		}
		var ids, symbols []string
		syms := map[string]bool{}
		for rows.Next() {
			var id string
			var purls, oldPURLs, current, previous []string
			if err := rows.Scan(&id, &purls, &oldPURLs, &current, &previous); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
			for _, v := range append(purls, oldPURLs...) {
				seen[v] = true
			}
			for _, v := range append(current, previous...) {
				if v != "" {
					syms[v] = true
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for v := range syms {
			symbols = append(symbols, v)
		}
		// A receipt can establish packages absent from its sample declaration.
		// Read complete validated sets, never a manifest-name approximation.
		rows, err = tx.Query(ctx, builderChangesReceiptsSQL, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var purls []string
			if err := rows.Scan(&purls); err != nil {
				rows.Close()
				return err
			}
			for _, v := range purls {
				seen[v] = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(symbols) == 0 {
			return nil
		}
		rows, err = tx.Query(ctx, builderChangesContendersSQL, symbols)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var declared, resolved []string
			if err := rows.Scan(&declared, &resolved); err != nil {
				return err
			}
			for _, v := range append(declared, resolved...) {
				seen[v] = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		return Changes{}, err
	}
	for v := range seen {
		out.SamplePURLs = append(out.SamplePURLs, v)
	}
	sort.Strings(out.SamplePURLs)
	sort.Slice(out.Targets, func(i, j int) bool {
		if out.Targets[i].PURL != out.Targets[j].PURL {
			return out.Targets[i].PURL < out.Targets[j].PURL
		}
		return out.Targets[i].Symbol < out.Targets[j].Symbol
	})
	return out, nil
}

const builderSelectedSamplesSQL = `
	SELECT sample_id FROM samples WHERE builder_coords && $1::text[]
	UNION SELECT sample_id FROM receipts WHERE builder_coords && $1::text[]`

// ListBuilderSamplesPage loads complete histories only for samples contributing
// to dirty packages. A receipt's undeclared package selects its sample too.
// Ordering is exactly ListSamplesPage's ordering, preserving ranking/tie breaks.
func (p *PG) ListBuilderSamplesPage(ctx context.Context, packages []BuilderPackage, limit, offset int) ([]SampleRow, error) {
	if len(packages) == 0 {
		return nil, nil
	}
	if limit <= 0 || offset < 0 {
		return nil, fmt.Errorf("serverstore: invalid builder sample page")
	}
	var out []SampleRow
	err := p.withBuilderRead(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, builderSamplesPageSQL, builderCoords(packages), limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanSample(rows)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListBuilderSnapshotTargets retains the global narrowest-claim rule while
// reading only compact claims sharing a symbol with a selected package. Raw
// manifests/receipts, and unrelated target keys, never cross this boundary.
func (p *PG) ListBuilderSnapshotTargets(ctx context.Context, packages []BuilderPackage) ([]SnapshotTarget, BuilderReadMetrics, error) {
	var metrics BuilderReadMetrics
	if len(packages) == 0 {
		return nil, metrics, nil
	}
	coords := builderCoords(packages)
	want := map[string]bool{}
	for _, c := range coords {
		want[c] = true
	}
	seen := map[SnapshotTarget]bool{}
	err := p.withBuilderRead(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, builderTargetEvidenceSQL, coords)
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
			metrics.Rows++
			encoded, _ := json.Marshal(t)
			metrics.Bytes += int64(len(encoded))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, builderTargetClaimsSQL, coords)
		if err != nil {
			return err
		}
		var claims []receiptClaim
		for rows.Next() {
			var c receiptClaim
			if err := rows.Scan(&c.Packages, &c.Symbols, &c.Subject); err != nil {
				rows.Close()
				return err
			}
			claims = append(claims, c)
			metrics.Rows++
			encoded, _ := json.Marshal(c)
			metrics.Bytes += int64(len(encoded))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, t := range snapshotTargetsFromClaims(claims) {
			p, err := domain.ParsePURL(t.PURL)
			if err != nil {
				continue
			}
			coord := (domain.PURL{Ecosystem: p.Ecosystem, Name: strings.ToLower(p.Name)}).String()
			if want[coord] {
				seen[t] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, metrics, err
	}
	out := make([]SnapshotTarget, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PURL != out[j].PURL {
			return out[i].PURL < out[j].PURL
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, metrics, nil
}

func (p *PG) BuilderSnapshotKeys(ctx context.Context, packages []BuilderPackage) ([]SnapshotTarget, error) {
	if len(packages) == 0 {
		return nil, nil
	}
	var out []SnapshotTarget
	err := p.withBuilderRead(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, builderSnapshotKeysSQL, builderCoords(packages))
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
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Keep complete production queries available to the real-PostgreSQL plan
// regression so predicate-only plans cannot conceal a corpus join scan.
const builderSamplesPageSQL = `SELECT ` + sampleCols + ` FROM samples
			WHERE sample_id IN (` + builderSelectedSamplesSQL + `) AND NOT quarantined
			ORDER BY created_at DESC, sample_id LIMIT $2 OFFSET $3`

const builderTargetClaimsSQL = `
			WITH selected AS MATERIALIZED (` + builderSelectedSamplesSQL + `),
			selected_symbols AS MATERIALIZED (
				SELECT DISTINCT unnest(s.builder_symbols) AS symbol FROM samples s
				JOIN selected USING(sample_id) WHERE NOT s.quarantined
			), contenders AS (
				SELECT sample_id FROM selected
				UNION SELECT s.sample_id FROM samples s
				WHERE s.builder_symbols && ARRAY(SELECT symbol FROM selected_symbols)
			)
			SELECT DISTINCT r.builder_packages,s.builder_symbols,s.builder_subject
			FROM contenders JOIN samples s USING(sample_id) JOIN receipts r USING(sample_id)
			WHERE NOT s.quarantined AND r.builder_claim`

const builderChangesSamplesSQL = `
			WITH changed AS MATERIALIZED (
				SELECT sample_id FROM samples WHERE created_at > $1
				UNION SELECT sample_id FROM samples WHERE updated_at > $1
				UNION SELECT sample_id FROM receipts WHERE created_at > $1
			)
			SELECT s.sample_id, s.builder_purls, s.builder_previous_purls,
			       s.builder_symbols, s.builder_previous_symbols
			FROM changed JOIN samples s USING(sample_id)`

const builderChangesContendersSQL = `
			SELECT DISTINCT s.builder_purls, r.builder_packages
			FROM samples s LEFT JOIN receipts r USING(sample_id)
			WHERE s.builder_symbols && $1::text[]`

const builderChangesEvidenceSQL = `SELECT DISTINCT purl, symbol FROM evidence_agg WHERE last_seen > $1`

const builderChangesReceiptsSQL = `SELECT builder_packages FROM receipts WHERE sample_id=ANY($1::text[])`

const builderTargetEvidenceSQL = `SELECT DISTINCT purl,symbol FROM evidence_agg
			WHERE builder_purl_coord(purl)=ANY($1::text[])`

const builderSnapshotKeysSQL = `SELECT purl,symbol FROM compatibility_snapshots
			WHERE builder_purl_coord(purl)=ANY($1::text[]) ORDER BY purl,symbol`
