package serverstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// BuilderPurlCoordFunctionSQL is the exact immutable SQL function definition
// required for expression index evaluation on purl coordinates.
const BuilderPurlCoordFunctionSQL = `CREATE OR REPLACE FUNCTION builder_purl_coord(raw TEXT) RETURNS TEXT
LANGUAGE SQL IMMUTABLE STRICT PARALLEL SAFE AS $$
  WITH parsed AS (
    SELECT regexp_match(raw, '^pkg:([^/]+)/(.+)@([^@]+)$') AS m
  ), decoded AS (
    SELECT m, convert_from(decode((
      SELECT string_agg(CASE WHEN left(token[1],1)='%' AND length(token[1])=3
        THEN substring(token[1] from 2)
        ELSE encode(convert_to(token[1], 'UTF8'), 'hex') END, '' ORDER BY n)
      FROM regexp_matches(m[2], '%[0-9A-Fa-f]{2}|[^%]', 'g') WITH ORDINALITY AS t(token,n)
    ), 'hex'), 'UTF8') AS name
    FROM parsed
    WHERE m IS NOT NULL AND right(m[2],1) <> '/'
      AND m[2] !~ '%([^0-9A-Fa-f]|[0-9A-Fa-f]([^0-9A-Fa-f]|$)|$)'
  )
  SELECT 'pkg:' || lower(m[1] COLLATE pg_catalog."pg_c_utf8") || '/' ||
    lower(name COLLATE pg_catalog."pg_c_utf8") || '@' FROM decoded
$$;`

// BuilderPrestageIndexSpec describes one heavy index that can be prepared
// concurrently before migration 0036 activation.
type BuilderPrestageIndexSpec struct {
	IndexName     string
	TableName     string
	ConcurrentSQL string
	ExpectedDef   string
}

// PrestageIndexSpecs lists all heavy B-tree indexes eligible for concurrent
// pre-activation staging outside the migration transaction.
var PrestageIndexSpecs = []BuilderPrestageIndexSpec{
	{
		IndexName:     "evidence_agg_builder_coord_idx",
		TableName:     "evidence_agg",
		ConcurrentSQL: `CREATE INDEX CONCURRENTLY IF NOT EXISTS evidence_agg_builder_coord_idx ON evidence_agg(builder_purl_coord(purl), purl, symbol)`,
		ExpectedDef:   `CREATE INDEX evidence_agg_builder_coord_idx ON evidence_agg USING btree (builder_purl_coord(purl), purl, symbol)`,
	},
	{
		IndexName:     "snapshots_builder_coord_idx",
		TableName:     "compatibility_snapshots",
		ConcurrentSQL: `CREATE INDEX CONCURRENTLY IF NOT EXISTS snapshots_builder_coord_idx ON compatibility_snapshots(builder_purl_coord(purl), purl, symbol)`,
		ExpectedDef:   `CREATE INDEX snapshots_builder_coord_idx ON compatibility_snapshots USING btree (builder_purl_coord(purl), purl, symbol)`,
	},
	{
		IndexName:     "evidence_agg_builder_changed_idx",
		TableName:     "evidence_agg",
		ConcurrentSQL: `CREATE INDEX CONCURRENTLY IF NOT EXISTS evidence_agg_builder_changed_idx ON evidence_agg(last_seen, purl, symbol)`,
		ExpectedDef:   `CREATE INDEX evidence_agg_builder_changed_idx ON evidence_agg USING btree (last_seen, purl, symbol)`,
	},
	{
		IndexName:     "samples_builder_created_idx",
		TableName:     "samples",
		ConcurrentSQL: `CREATE INDEX CONCURRENTLY IF NOT EXISTS samples_builder_created_idx ON samples(created_at, sample_id)`,
		ExpectedDef:   `CREATE INDEX samples_builder_created_idx ON samples USING btree (created_at, sample_id)`,
	},
}

var indexSchemaQualRe = regexp.MustCompile(`(?i)\bON\s+(?:[a-zA-Z0-9_"]+\.)?([a-zA-Z0-9_"]+)\s+USING\s+`)

func normalizeIndexDef(raw string) string {
	s := indexSchemaQualRe.ReplaceAllString(raw, " ON $1 USING ")
	return strings.Join(strings.Fields(s), " ")
}

// PrestageBuilderIndexes prepares the builder_purl_coord function and heavy
// B-tree indexes concurrently outside a transaction block. Any interrupted or
// invalid indexes from previous attempts are detected, dropped, and rebuilt.
// Safe to run repeatedly while the prior release remains active.
func PrestageBuilderIndexes(ctx context.Context, conn *pgx.Conn) error {
	// 1. Create or replace the immutable helper function.
	if _, err := conn.Exec(ctx, BuilderPurlCoordFunctionSQL); err != nil {
		return fmt.Errorf("serverstore: create builder_purl_coord function: %w", err)
	}

	// 2. Validate function definition and functional behavior.
	if err := validateBuilderPurlCoordFunction(ctx, conn); err != nil {
		return err
	}

	// 3. For each heavy index, inspect existing state, cleanup invalid/interrupted builds, and build concurrently.
	for _, spec := range PrestageIndexSpecs {
		if err := ctx.Err(); err != nil {
			return err
		}

		info, exists, err := queryIndexInfo(ctx, conn, spec.IndexName)
		if err != nil {
			return fmt.Errorf("serverstore: query index %s: %w", spec.IndexName, err)
		}

		if exists {
			if !info.IsValid || !info.IsReady {
				// Interrupted or invalid index from a previous run: drop cleanly before retrying.
				dropSQL := fmt.Sprintf("DROP INDEX CONCURRENTLY IF EXISTS %s", spec.IndexName)
				if _, err := conn.Exec(ctx, dropSQL); err != nil {
					// Fallback to non-concurrent drop if concurrent drop fails.
					if _, errFallback := conn.Exec(ctx, fmt.Sprintf("DROP INDEX IF EXISTS %s", spec.IndexName)); errFallback != nil {
						return fmt.Errorf("serverstore: drop invalid index %s: %w", spec.IndexName, err)
					}
				}
				exists = false
			} else {
				// Valid index already exists: verify definition exact match.
				if err := validateIndexDefinition(spec, info); err != nil {
					return err
				}
			}
		}

		if !exists {
			if _, err := conn.Exec(ctx, spec.ConcurrentSQL); err != nil {
				return fmt.Errorf("serverstore: concurrent build %s: %w", spec.IndexName, err)
			}
		}
	}

	// 4. Final strict validation of all prebuilt indexes.
	return ValidateBuilderPrebuiltIndexes(ctx, conn, true)
}

type indexInfo struct {
	IndexName string
	TableName string
	IsValid   bool
	IsReady   bool
	Def       string
}

func queryIndexInfo(ctx context.Context, conn *pgx.Conn, name string) (indexInfo, bool, error) {
	query := `SELECT c.relname, t.relname, i.indisvalid, i.indisready, pg_get_indexdef(c.oid)
		FROM pg_class c
		JOIN pg_index i ON i.indexrelid = c.oid
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = $1`

	var info indexInfo
	err := conn.QueryRow(ctx, query, name).Scan(&info.IndexName, &info.TableName, &info.IsValid, &info.IsReady, &info.Def)
	if err == pgx.ErrNoRows {
		return indexInfo{}, false, nil
	}
	if err != nil {
		return indexInfo{}, false, err
	}
	return info, true, nil
}

func validateIndexDefinition(spec BuilderPrestageIndexSpec, info indexInfo) error {
	if !info.IsValid || !info.IsReady {
		return fmt.Errorf("serverstore: prebuilt index %s is invalid (indisvalid=%v, indisready=%v); clean up or rebuild",
			spec.IndexName, info.IsValid, info.IsReady)
	}
	got := normalizeIndexDef(info.Def)
	want := normalizeIndexDef(spec.ExpectedDef)
	if got != want {
		return fmt.Errorf("serverstore: prebuilt index %s definition mismatch: got %q, want %q",
			spec.IndexName, got, want)
	}
	return nil
}

// ValidateBuilderPrebuiltIndexes verifies that builder projection prebuilt
// objects in the current schema are valid and match their exact definitions.
// If requireAll is true, all PrestageIndexSpecs and builder_purl_coord must exist.
// If requireAll is false, any object that exists must be valid and exact.
func ValidateBuilderPrebuiltIndexes(ctx context.Context, conn *pgx.Conn, requireAll bool) error {
	var funcExists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = current_schema() AND p.proname = 'builder_purl_coord')`).Scan(&funcExists); err != nil {
		return fmt.Errorf("serverstore: check builder_purl_coord: %w", err)
	}

	if funcExists {
		if err := validateBuilderPurlCoordFunction(ctx, conn); err != nil {
			return err
		}
	} else if requireAll {
		return fmt.Errorf("serverstore: required function builder_purl_coord is missing")
	}

	for _, spec := range PrestageIndexSpecs {
		info, exists, err := queryIndexInfo(ctx, conn, spec.IndexName)
		if err != nil {
			return fmt.Errorf("serverstore: inspect index %s: %w", spec.IndexName, err)
		}
		if !exists {
			if requireAll {
				return fmt.Errorf("serverstore: required prebuilt index %s is missing", spec.IndexName)
			}
			continue
		}
		if err := validateIndexDefinition(spec, info); err != nil {
			return err
		}
	}
	return nil
}

func validateBuilderPurlCoordFunction(ctx context.Context, conn *pgx.Conn) error {
	var provolatile, proparallel string
	var proisstrict bool
	var rettype, args, prosrc string
	query := `SELECT p.provolatile::text, p.proparallel::text, p.proisstrict,
		pg_get_function_result(p.oid), pg_get_function_arguments(p.oid), p.prosrc
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = current_schema() AND p.proname = 'builder_purl_coord'`
	if err := conn.QueryRow(ctx, query).Scan(&provolatile, &proparallel, &proisstrict, &rettype, &args, &prosrc); err != nil {
		return fmt.Errorf("serverstore: inspect builder_purl_coord: %w", err)
	}

	if provolatile != "i" {
		return fmt.Errorf("serverstore: builder_purl_coord must be IMMUTABLE, got provolatile=%q", provolatile)
	}
	if proparallel != "s" {
		return fmt.Errorf("serverstore: builder_purl_coord must be PARALLEL SAFE, got proparallel=%q", proparallel)
	}
	if !proisstrict {
		return fmt.Errorf("serverstore: builder_purl_coord must be STRICT")
	}
	if strings.ToLower(rettype) != "text" {
		return fmt.Errorf("serverstore: builder_purl_coord return type must be text, got %q", rettype)
	}
	if !strings.Contains(strings.ToLower(args), "raw text") && strings.ToLower(args) != "text" {
		return fmt.Errorf("serverstore: builder_purl_coord arguments must be raw text, got %q", args)
	}

	// Verify semantic correctness through direct sample conversions.
	for _, tc := range []struct {
		input, expected string
	}{
		{"pkg:npm/%40scope/package@1.0.0", "pkg:npm/@scope/package@"},
		{"pkg:GOLANG/github.com/jackc/pgx@v5.10.0", "pkg:golang/github.com/jackc/pgx@"},
		{"pkg:pypi/Requests@2.31.0", "pkg:pypi/requests@"},
		{"pkg:npm/foo%2Fbar@1.0.0", "pkg:npm/foo/bar@"},
	} {
		var got *string
		if err := conn.QueryRow(ctx, `SELECT builder_purl_coord($1)`, tc.input).Scan(&got); err != nil {
			return fmt.Errorf("serverstore: test builder_purl_coord(%q): %w", tc.input, err)
		}
		if got == nil || *got != tc.expected {
			return fmt.Errorf("serverstore: builder_purl_coord(%q) = %v, want %q", tc.input, got, tc.expected)
		}
	}
	var nilResult *string
	if err := conn.QueryRow(ctx, `SELECT builder_purl_coord(NULL)`).Scan(&nilResult); err != nil {
		return fmt.Errorf("serverstore: test builder_purl_coord(NULL): %w", err)
	}
	if nilResult != nil {
		return fmt.Errorf("serverstore: builder_purl_coord(NULL) must be NULL, got %q", *nilResult)
	}

	return nil
}

// validatePrebuiltBuilderObjects is called before applying migration 0036.
// Any prebuilt index or function must be strictly valid and match the required
// definition. If none exist yet, validation passes so 0036 can build them.
func validatePrebuiltBuilderObjects(ctx context.Context, conn *pgx.Conn) error {
	return ValidateBuilderPrebuiltIndexes(ctx, conn, false)
}

// PrestageBuilderIndexes prepares heavy indexes on PG.
func (p *PG) PrestageBuilderIndexes(ctx context.Context) error {
	return p.withConn(ctx, func(conn *pgx.Conn) error {
		return PrestageBuilderIndexes(ctx, conn)
	})
}

// ValidateBuilderPrebuiltIndexes verifies prebuilt index state on PG.
func (p *PG) ValidateBuilderPrebuiltIndexes(ctx context.Context, requireAll bool) error {
	return p.withConn(ctx, func(conn *pgx.Conn) error {
		return ValidateBuilderPrebuiltIndexes(ctx, conn, requireAll)
	})
}
