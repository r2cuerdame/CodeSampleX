package deploygate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func builderProjectionMigrationSQL(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration fixture")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "serverstore", "migrations", "0036_builder_projections.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

func TestBuilderProjectionMigrationMatchesReviewedDigest(t *testing.T) {
	sql := builderProjectionMigrationSQL(t)
	for _, body := range []string{sql, strings.ReplaceAll(sql, "\n", "\r\n")} {
		if err := ValidateMigrationSQL("0036_builder_projections.sql", body); err != nil {
			t.Fatalf("reviewed migration rejected: %v", err)
		}
	}
}

func TestBuilderProjectionMigrationDigestExceptionFailsClosed(t *testing.T) {
	sql := builderProjectionMigrationSQL(t)
	mutate := func(old, replacement string) string {
		t.Helper()
		if !strings.Contains(sql, old) {
			t.Fatalf("missing mutation input %q", old)
		}
		return strings.Replace(sql, old, replacement, 1)
	}
	for name, body := range map[string]string{
		"wrong filename":        sql,
		"function renamed":      mutate("CREATE FUNCTION builder_purl_coord", "CREATE FUNCTION unsafe_coord"),
		"source table":          mutate("ALTER TABLE samples", "ALTER TABLE receipts"),
		"source JSON":           mutate("md5(manifest::text)", "md5(receipt::text)"),
		"index setting":         mutate("fastupdate=off", "fastupdate=on"),
		"index setting removed": mutate(" WITH (fastupdate=off)", ""),
		"index renamed":         mutate("samples_builder_coords_idx", "arbitrary_idx"),
		"function authority":    mutate("LANGUAGE SQL IMMUTABLE", "LANGUAGE SQL SECURITY DEFINER IMMUTABLE"),
		"function search path":  mutate("LANGUAGE SQL IMMUTABLE", "LANGUAGE SQL SET search_path=public IMMUTABLE"),
		"function body":         mutate("SELECT 'pkg:'", "SELECT 'unsafe:'"),
		"collation downgraded":  mutate(`pg_catalog."pg_c_utf8"`, `pg_catalog."C"`),
		"collation removed":     mutate(` COLLATE pg_catalog."pg_c_utf8"`, ""),
		"statement removed":     mutate("CREATE INDEX samples_builder_created_idx ON samples(created_at, sample_id);", ""),
		"destructive appended":  sql + "\nDROP TABLE receipts;",
		"additive appended":     sql + "\nALTER TABLE samples ADD COLUMN innocent TEXT;",
		"comment changed":       sql + "\n-- changed release artifact\n",
		"bare CR":               strings.ReplaceAll(sql, "\n", "\r"),
	} {
		t.Run(name, func(t *testing.T) {
			filename := "0036_builder_projections.sql"
			if name == "wrong filename" {
				filename = "0099_builder_projections.sql"
			}
			if err := ValidateMigrationSQL(filename, body); err == nil {
				t.Fatal("changed migration passed the pinned exception")
			}
		})
	}
	// Function support remains exclusive to the exact reviewed artifact.
	if err := ValidateMigrationSQL("0099_function.sql",
		"CREATE FUNCTION arbitrary() RETURNS text LANGUAGE SQL IMMUTABLE AS $$ SELECT 'ok' $$;"); err == nil {
		t.Fatal("general validator learned arbitrary SQL functions")
	}
}
