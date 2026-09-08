package serverstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestIntegrationBuilderCoordinateKeysMatchGoOrFailClosed(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	cases := []struct {
		raw, want string
	}{
		{"pkg:npm/axios@1.2.3", "pkg:npm/axios@"},
		{"pkg:NPM/Axios@1.2.3", "pkg:npm/axios@"},
		{"pkg:npm/@Scope/Name@1.2.3", "pkg:npm/%40scope/name@"},
		{"pkg:npm/%40Scope/Name@1.2.3", "pkg:npm/%40scope/name@"},
		{"pkg:golang/github.com/Owner/Module/v2@2.0.0", "pkg:golang/github.com/owner/module/v2@"},
		{"pkg:maven/org.example:Library@1.0.0", "pkg:maven/org.example:library@"},
		{"pkg:npm/a@b@1.0.0", "pkg:npm/a@b@"},
		{"pkg:npm/%40@1", "pkg:npm/%40@"},
		{"pkg:npm/a@%bad-version", "pkg:npm/a@"},
		{"pkg:npm/%61xios@1.2.3", "!"},
		{"pkg:npm/scope%2fname@1.2.3", "!"},
		{"pkg:npm/%40scope/%6eame@1.2.3", "!"},
		{"pkg:npm/%2540scope/name@1.2.3", "!"},
		{"pkg:npm/name%ZZ@1.2.3", "!"},
		{"pkg:npm/café@1.2.3", "!"},
		{"pkg:NPİM/name@1.2.3", "!"},
		{"", ""},
		{"pkg:npm/no-version", ""},
		{"pkg:npm/name@", ""},
		{"pkg:npm/@1", ""},
		{"pkg:/name@1", ""},
		{"pkg:npmname@1", ""},
		{"pkg:npm/name/@1", ""},
		{"PKG:npm/name@1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			var got string
			err := pg.withConn(ctx, func(c *pgx.Conn) error {
				return c.QueryRow(ctx, "SELECT csx_builder_coord($1)", tc.raw).Scan(&got)
			})
			if err != nil || got != tc.want {
				t.Fatalf("coord(%q) = %q, err=%v; want %q", tc.raw, got, err, tc.want)
			}
			if got == "" || got == "!" {
				return
			}
			p, err := domain.ParsePURL(tc.raw)
			if err != nil {
				t.Fatalf("SQL accepted an invalid Go identity: %v", err)
			}
			p.Version = ""
			if want := strings.ToLower(p.String()); got != want {
				t.Fatalf("SQL key %q differs from Go key %q", got, want)
			}
		})
	}
	var nullKey string
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, "SELECT csx_builder_coord(NULL)").Scan(&nullKey)
	}); err != nil || nullKey != "" {
		t.Fatalf("NULL coordinate = %q, err=%v", nullKey, err)
	}
}

func TestIntegrationBuilderCoordinateArraysGuardLegacyJSONShapes(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	cases := []struct {
		raw  string
		want []string
	}{
		{"null", []string{}},
		{"{}", []string{}},
		{`"pkg:npm/axios@1"`, []string{}},
		{"[]", []string{}},
		{`[null,12,{},[],"missing-version"]`, []string{}},
		{`["pkg:npm/b@2","pkg:npm/a@1","pkg:npm/b@3"]`, []string{"pkg:npm/a@", "pkg:npm/b@"}},
		{`["pkg:npm/@scope/name@1","pkg:npm/%40scope/name@2"]`, []string{"pkg:npm/%40scope/name@"}},
		{`["pkg:npm/%61@1","pkg:npm/b@2",null,42]`, []string{"!", "pkg:npm/b@"}},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			var got []string
			err := pg.withConn(ctx, func(c *pgx.Conn) error {
				return c.QueryRow(ctx, "SELECT csx_builder_coords($1::jsonb)", tc.raw).Scan(&got)
			})
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("coords(%s) = %v, err=%v; want %v", tc.raw, got, err, tc.want)
			}
		})
	}
}

func TestIntegrationBuilderCandidateIndexesTrackDirectWrites(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `
			INSERT INTO samples(sample_id, manifest, status, license, size_bytes)
			VALUES ('scope-sample', '{"packages":["pkg:npm/declared@1"]}', 'PUBLISHED', 'MIT-0', 1)
		`); err != nil {
			return err
		}
		if _, err := c.Exec(ctx, `
			INSERT INTO receipts(receipt_id, sample_id, peer_id, env_hash, receipt, contract_result)
			VALUES ('scope-receipt', 'scope-sample', 'scope-peer', 'scope-env',
				'{"schemaVersion":2,"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/undeclared@2"]}', 'PASS')
		`); err != nil {
			return err
		}
		// The indexed candidate mapping follows the receipt itself, including
		// packages the sample never declared. HTTP-only invariants cannot hide it.
		var id string
		if err := c.QueryRow(ctx, `
			SELECT sample_id FROM receipts
			WHERE csx_builder_coords(receipt->'resolvedPackages') && ARRAY['pkg:npm/undeclared@']
		`).Scan(&id); err != nil || id != "scope-sample" {
			return fmt.Errorf("undeclared receipt candidate = %q, err=%v", id, err)
		}
		// An out-of-band legacy repair must immediately enter the fail-closed
		// set, without a migration rerun or a projection synchronization pass.
		if _, err := c.Exec(ctx, `
			UPDATE samples SET manifest='{"packages":["pkg:npm/%64eclared@1"]}'
			WHERE sample_id='scope-sample'
		`); err != nil {
			return err
		}
		if err := c.QueryRow(ctx, `
			SELECT sample_id FROM samples
			WHERE csx_builder_coords(manifest->'packages') && ARRAY['!']
		`).Scan(&id); err != nil || id != "scope-sample" {
			return fmt.Errorf("ambiguous legacy candidate = %q, err=%v", id, err)
		}
		var indexes int
		if err := c.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema()
			  AND indexname = ANY(ARRAY[
				'builder_samples_packages_idx','builder_receipts_packages_idx',
				'builder_samples_subject_idx','builder_samples_symbols_idx',
				'builder_evidence_coord_idx','builder_snapshots_coord_idx'])
		`).Scan(&indexes); err != nil {
			return err
		}
		if indexes != 6 {
			return fmt.Errorf("builder candidate indexes = %d, want 6", indexes)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
