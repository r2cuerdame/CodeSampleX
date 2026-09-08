package serverstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

const builderProjectionBatch = 256

type sampleBuilderProjection struct {
	coords, purls, symbols []string
	subject                string
}
type receiptBuilderProjection struct {
	packages, coords []string
	claim            bool
}

func builderCoord(p domain.PURL) string {
	return (domain.PURL{Ecosystem: p.Ecosystem, Name: strings.ToLower(p.Name)}).String()
}

func sortedBuilderStrings(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// Derive from the same typed manifests and whole-list receipt validation as
// aggregation. In particular a CLI receipt may resolve an undeclared package.
func deriveSampleBuilderProjection(raw string) (sampleBuilderProjection, error) {
	var manifest domain.SampleManifest
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return sampleBuilderProjection{}, err
	}
	coords, purls, symbols := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, raw := range append(append([]string(nil), manifest.Packages...), manifest.Subject) {
		if p, err := domain.ParsePURL(raw); err == nil {
			coords[builderCoord(p)], purls[p.String()] = true, true
		}
	}
	for _, symbol := range manifest.Symbols {
		if symbol != "" {
			symbols[symbol] = true
		}
	}
	return sampleBuilderProjection{coords: sortedBuilderStrings(coords),
		purls: sortedBuilderStrings(purls), symbols: sortedBuilderStrings(symbols),
		subject: manifest.Subject}, nil
}

func deriveReceiptBuilderProjection(raw string) (receiptBuilderProjection, error) {
	var receipt domain.VerificationReceipt
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
		return receiptBuilderProjection{}, err
	}
	packages := resolvedPackageStrings(raw)
	if packages == nil {
		packages = []string{}
	}
	coords := map[string]bool{}
	for _, raw := range packages {
		p, _ := domain.ParsePURL(raw) // validated as one complete list above
		coords[builderCoord(p)] = true
	}
	return receiptBuilderProjection{packages: packages, coords: sortedBuilderStrings(coords),
		claim: receiptEstablishesClaim(raw)}, nil
}

// Preserve SaveSample's ability to retain legacy rows. An ambiguous row has
// no trusted hash and blocks incremental reads instead of losing its evidence.
func maintainSampleBuilderProjection(ctx context.Context, tx pgx.Tx, id, raw string) error {
	projection, err := deriveSampleBuilderProjection(raw)
	if err != nil {
		_, err = tx.Exec(ctx, `UPDATE samples SET builder_source_hash=NULL WHERE sample_id=$1`, id)
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE samples SET
		builder_previous_purls=CASE WHEN builder_source_hash IS DISTINCT FROM md5(manifest::text)
			THEN ARRAY(SELECT DISTINCT x FROM unnest(builder_previous_purls || builder_purls) x ORDER BY x)
			ELSE builder_previous_purls END,
		builder_previous_symbols=CASE WHEN builder_source_hash IS DISTINCT FROM md5(manifest::text)
			THEN ARRAY(SELECT DISTINCT x FROM unnest(builder_previous_symbols || builder_symbols) x ORDER BY x)
			ELSE builder_previous_symbols END,
		builder_coords=$2, builder_purls=$3, builder_symbols=$4, builder_subject=$5,
		builder_source_hash=md5(manifest::text)
		WHERE sample_id=$1`, id, projection.coords, projection.purls,
		projection.symbols, projection.subject)
	return err
}

func maintainReceiptBuilderProjection(ctx context.Context, tx pgx.Tx, id, raw string) error {
	projection, err := deriveReceiptBuilderProjection(raw)
	if err != nil {
		_, err = tx.Exec(ctx, `UPDATE receipts SET builder_source_hash=NULL WHERE receipt_id=$1`, id)
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE receipts SET builder_packages=$2, builder_coords=$3,
		builder_claim=$4, builder_source_hash=md5(receipt::text) WHERE receipt_id=$1`,
		id, projection.packages, projection.coords, projection.claim)
	return err
}

// Each caller invokes this inside the same repeatable-read transaction as its
// scoped reads. Old binaries and direct SQL cannot silently create omissions.
func checkBuilderProjections(ctx context.Context, tx pgx.Tx) error {
	var stale bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM samples WHERE builder_source_hash IS DISTINCT FROM md5(manifest::text)
		UNION ALL
		SELECT 1 FROM receipts WHERE builder_source_hash IS DISTINCT FROM md5(receipt::text)
	)`).Scan(&stale); err != nil {
		return err
	}
	if stale {
		return fmt.Errorf("serverstore: builder projection is stale or ambiguous; run migrations to repair before incremental aggregation")
	}
	return nil
}

// Backfill only the indexed stale subset. A failed/cancelled page rolls back;
// successful pages are safe to resume. There is no whole-corpus fallback in
// incremental execution, and source/projection changes commit together.
func backfillBuilderProjections(ctx context.Context, conn *pgx.Conn) error {
	for _, sample := range []bool{true, false} {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			count, err := backfillBuilderProjectionPage(ctx, conn, sample)
			if err != nil {
				return err
			}
			if count < builderProjectionBatch {
				break
			}
		}
	}
	return nil
}

func backfillBuilderProjectionPage(ctx context.Context, conn *pgx.Conn, sample bool) (int, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	query := `SELECT receipt_id, receipt::text, builder_source_hash
		FROM receipts WHERE builder_source_hash IS DISTINCT FROM md5(receipt::text)
		ORDER BY receipt_id LIMIT $1 FOR UPDATE`
	if sample {
		query = `SELECT sample_id, manifest::text, builder_source_hash
			FROM samples WHERE builder_source_hash IS DISTINCT FROM md5(manifest::text)
			ORDER BY sample_id LIMIT $1 FOR UPDATE`
	}
	rows, err := tx.Query(ctx, query, builderProjectionBatch)
	if err != nil {
		return 0, err
	}
	type source struct {
		id, raw string
		hash    *string
	}
	var sources []source
	for rows.Next() {
		var row source
		if err := rows.Scan(&row.id, &row.raw, &row.hash); err != nil {
			rows.Close()
			return 0, err
		}
		sources = append(sources, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, row := range sources {
		if sample {
			if _, err := deriveSampleBuilderProjection(row.raw); err != nil {
				return 0, fmt.Errorf("serverstore: ambiguous builder sample projection %s: %w", row.id, err)
			}
			if err := maintainSampleBuilderProjection(ctx, tx, row.id, row.raw); err != nil {
				return 0, err
			}
			// Replayed/old-binary updates must invalidate their old and new
			// coordinates even when the old writer did not update the clock.
			if _, err := tx.Exec(ctx, `UPDATE samples SET updated_at=now() WHERE sample_id=$1`, row.id); err != nil {
				return 0, err
			}
		} else {
			if row.hash != nil {
				return 0, fmt.Errorf("serverstore: changed immutable receipt %s has an ambiguous builder projection", row.id)
			}
			if _, err := deriveReceiptBuilderProjection(row.raw); err != nil {
				return 0, fmt.Errorf("serverstore: ambiguous builder receipt projection %s: %w", row.id, err)
			}
			if err := maintainReceiptBuilderProjection(ctx, tx, row.id, row.raw); err != nil {
				return 0, err
			}
			if _, err := tx.Exec(ctx, `UPDATE samples SET updated_at=now()
				WHERE sample_id=(SELECT sample_id FROM receipts WHERE receipt_id=$1)`, row.id); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(sources), nil
}
