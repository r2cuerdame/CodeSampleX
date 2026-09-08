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

// BuilderClaimReadObserver exposes selected claim rows/JSON volume to the
// builder's phase recorder, including failed/cancelled reads.
type BuilderClaimReadObserver func() func(rows, jsonBytes int64, err error)
type builderClaimObserverKey struct{}

func WithBuilderClaimReadObserver(ctx context.Context, observer BuilderClaimReadObserver) context.Context {
	return context.WithValue(ctx, builderClaimObserverKey{}, observer)
}

// IncrementalBuilderStore narrows source acquisition, never evidence validation.
// Stores without this capability retain the full reference implementation.
type IncrementalBuilderStore interface {
	BuilderPackageScope(context.Context, time.Time, Changes) ([]string, error)
	ListSnapshotTargetsForPackages(context.Context, []string) ([]SnapshotTarget, error)
	ListBuilderSamplesPage(context.Context, []string, int, int) ([]SampleRow, error)
	SnapshotKeysForPackages(context.Context, []string) ([]SnapshotTarget, error)
}

func builderCoord(raw string) string {
	p, err := domain.ParsePURL(raw)
	if err != nil {
		return ""
	}
	p.Version = ""
	return strings.ToLower(p.String())
}

func sortedCoords(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		if s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Expression indexes cover existing rows and all writers, including direct SQL.
// Legacy escaping that SQL cannot safely equate with Go's PathUnescape must
// stop the pass BEFORE writes. It is never treated as absent evidence.
func (p *PG) checkBuilderCoordinates(ctx context.Context) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		var source, id string
		err := c.QueryRow(ctx, `
   SELECT source, id FROM (
    (SELECT 'manifest keys' AS source, sample_id AS id FROM samples
      WHERE csx_builder_unsafe_keys(manifest) LIMIT 1)
    UNION ALL
    (SELECT 'receipt keys', receipt_id FROM receipts
      WHERE csx_builder_unsafe_keys(receipt) LIMIT 1)
    UNION ALL
    (SELECT 'sample' AS source, sample_id AS id FROM samples
      WHERE csx_builder_coords(manifest->'packages') @> ARRAY['!'] LIMIT 1)
    UNION ALL
    (SELECT 'subject', sample_id FROM samples
      WHERE csx_builder_coord(manifest->>'subject') = '!' LIMIT 1)
    UNION ALL
    (SELECT 'receipt', receipt_id FROM receipts
      WHERE csx_builder_coords(receipt->'resolvedPackages') @> ARRAY['!'] LIMIT 1)
    UNION ALL
    (SELECT 'evidence', purl FROM evidence_agg WHERE csx_builder_coord(purl) = '!' LIMIT 1)
    UNION ALL
    (SELECT 'snapshot', purl FROM compatibility_snapshots WHERE csx_builder_coord(purl) = '!' LIMIT 1)
   ) unsafe LIMIT 1`).Scan(&source, &id)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("ambiguous legacy builder coordinate in %s %q; full repair remains available; canonicalize the source before incremental aggregation", source, id)
	})
}

// BuilderPackageScope expands dirty packages through changed symbol claims.
// A quarantined narrow claim can restore another package's symbol even when
// that other package has no new receipts. Stored symbols retain the old side.
func (p *PG) BuilderPackageScope(ctx context.Context, since time.Time, changes Changes) ([]string, error) {
	if err := p.checkBuilderCoordinates(ctx); err != nil {
		return nil, err
	}
	coords := map[string]bool{}
	for _, t := range changes.Targets {
		coords[builderCoord(t.PURL)] = true
	}
	for _, raw := range changes.SamplePURLs {
		coords[builderCoord(raw)] = true
	}
	symbols := map[string]bool{}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
   SELECT manifest->'symbols' FROM samples WHERE sample_id IN (
    SELECT sample_id FROM samples WHERE created_at > $1
    UNION SELECT sample_id FROM samples WHERE updated_at > $1
    UNION SELECT sample_id FROM receipts WHERE created_at > $1
   )`, since)
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			var names []string
			if json.Unmarshal(raw, &names) == nil {
				for _, s := range names {
					if s != "" {
						symbols[s] = true
					}
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = c.Query(ctx, `SELECT DISTINCT symbol FROM compatibility_snapshots
   WHERE csx_builder_coord(purl) = ANY($1) AND symbol <> ''`, sortedCoords(coords))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			symbols[s] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	// Include quarantined claims here to invalidate their former ownership.
	claims, err := p.builderClaims(ctx, sortedCoords(coords), nil, true)
	if err != nil {
		return nil, err
	}
	for _, claim := range claims {
		for _, s := range claim.Symbols {
			if s != "" {
				symbols[s] = true
			}
		}
	}
	if len(symbols) > 0 {
		peers, err := p.builderClaims(ctx, nil, sortedCoords(symbols), true)
		if err != nil {
			return nil, err
		}
		claims = append(claims, peers...)
	}
	for _, claim := range claims {
		coords[builderCoord(claim.Subject)] = true
		for _, raw := range claim.Packages {
			coords[builderCoord(raw)] = true
		}
	}
	return sortedCoords(coords), nil
}

// builderClaims reads only relevant receipt claims. Matching is deliberately
// a superset: resolvedPackageStrings still validates the ENTIRE signed list.
// In particular, declared manifest packages never authorize receipt attribution.
func (p *PG) builderClaims(ctx context.Context, coords, symbols []string, includeQuarantined bool) ([]receiptClaim, error) {
	if len(coords) == 0 && len(symbols) == 0 {
		return nil, nil
	}
	var out []receiptClaim
	var readRows, readBytes int64
	var finish func(int64, int64, error)
	if observer, ok := ctx.Value(builderClaimObserverKey{}).(BuilderClaimReadObserver); ok {
		finish = observer()
	}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, builderClaimsSQL, nonNilStrings(coords), nonNilStrings(symbols), includeQuarantined)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			var manifest []byte
			if err := rows.Scan(&raw, &manifest); err != nil {
				return err
			}
			readRows++
			readBytes += int64(len(raw) + len(manifest))
			var claim receiptClaim
			if json.Unmarshal(manifest, &claim) != nil {
				continue
			}
			claim.Packages = resolvedPackageStrings(raw)
			out = append(out, claim)
		}
		return rows.Err()
	})
	if finish != nil {
		finish(readRows, readBytes, err)
	}
	return out, err
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (p *PG) ListSnapshotTargetsForPackages(ctx context.Context, coords []string) ([]SnapshotTarget, error) {
	seen := map[SnapshotTarget]bool{}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT DISTINCT purl, symbol FROM evidence_agg
   WHERE csx_builder_coord(purl) = ANY($1)`, nonNilStrings(coords))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var target SnapshotTarget
			if err := rows.Scan(&target.PURL, &target.Symbol); err != nil {
				return err
			}
			seen[target] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	claims, err := p.builderClaims(ctx, coords, nil, false)
	if err != nil {
		return nil, err
	}
	symbols := map[string]bool{}
	for _, claim := range claims {
		for _, s := range claim.Symbols {
			if s != "" {
				symbols[s] = true
			}
		}
	}
	if len(symbols) > 0 {
		peers, err := p.builderClaims(ctx, nil, sortedCoords(symbols), false)
		if err != nil {
			return nil, err
		}
		claims = append(claims, peers...)
	}
	scope := map[string]bool{}
	for _, coord := range coords {
		scope[coord] = true
	}
	for _, target := range snapshotTargetsFromClaims(claims) {
		if scope[builderCoord(target.PURL)] {
			seen[target] = true
		}
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

func (p *PG) ListBuilderSamplesPage(ctx context.Context, coords []string, limit, offset int) ([]SampleRow, error) {
	var out []SampleRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, builderSamplesSQL, nonNilStrings(coords), limit, offset)
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

func (p *PG) SnapshotKeysForPackages(ctx context.Context, coords []string) ([]SnapshotTarget, error) {
	var out []SnapshotTarget
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT purl, symbol FROM compatibility_snapshots
   WHERE csx_builder_coord(purl) = ANY($1) ORDER BY purl, symbol`, nonNilStrings(coords))
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

const builderClaimsSQL = `
   SELECT r.receipt::text, jsonb_build_object('symbols', s.manifest->'symbols', 'subject', s.manifest->'subject')
   FROM receipts r JOIN samples s ON s.sample_id = r.sample_id
   WHERE ($3 OR NOT s.quarantined)
    AND r.receipt->>'schemaVersion' = '2'
    AND r.receipt->'stages'->>'resolve' = 'PASS'
    AND r.receipt_id IN (
     SELECT receipt_id FROM receipts WHERE csx_builder_coords(receipt->'resolvedPackages') && $1
     UNION
     SELECT r2.receipt_id FROM samples s2 JOIN receipts r2 ON r2.sample_id = s2.sample_id
       WHERE csx_builder_coord(s2.manifest->>'subject') = ANY($1)
     UNION
     SELECT r3.receipt_id FROM samples s3 JOIN receipts r3 ON r3.sample_id = s3.sample_id
       WHERE s3.manifest->'symbols' ?| $2
    )
   ORDER BY r.receipt_id`

const builderSamplesSQL = `SELECT ` + sampleCols + ` FROM samples
   WHERE NOT quarantined AND sample_id IN (
    SELECT sample_id FROM samples WHERE csx_builder_coords(manifest->'packages') && $1
    UNION SELECT sample_id FROM receipts WHERE csx_builder_coords(receipt->'resolvedPackages') && $1
    UNION SELECT sample_id FROM samples WHERE csx_builder_coord(manifest->>'subject') = ANY($1)
   ) ORDER BY created_at DESC, sample_id LIMIT $2 OFFSET $3`
