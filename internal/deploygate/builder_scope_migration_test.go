package deploygate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func reviewedBuilderScopeMigration(t *testing.T) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate builder scope migration test")
	}
	path := filepath.Join(filepath.Dir(testFile), "..", "serverstore", "migrations", "0036_builder_scope_indexes.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read actual builder scope migration: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

func TestReviewedBuilderScopeMigrationPassesOnlyItsNamedGate(t *testing.T) {
	sql := reviewedBuilderScopeMigration(t)
	for name, body := range map[string]string{
		"LF":   sql,
		"CRLF": strings.ReplaceAll(sql, "\n", "\r\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateMigrationSQL("0036_builder_scope_indexes.sql", body); err != nil {
				t.Fatalf("reviewed migration rejected: %v", err)
			}
		})
	}
	for _, name := range []string{"0099_builder_scope_indexes.sql", "0036_other.sql", "0036_builder_scope_indexes.SQL"} {
		if err := ValidateMigrationSQL(name, sql); err == nil {
			t.Errorf("builder scope exception escaped its exact filename: %s", name)
		}
	}
}

func TestBuilderScopeMigrationDigestRejectsChangedAuthorityAndStatements(t *testing.T) {
	sql := reviewedBuilderScopeMigration(t)
	replace := func(old, replacement string) string {
		t.Helper()
		if !strings.Contains(sql, old) {
			t.Fatalf("mutation fixture no longer matches reviewed migration: %q", old)
		}
		return strings.Replace(sql, old, replacement, 1)
	}
	cases := map[string]string{
		"empty":                                "",
		"unsafe coordinate sentinel":           replace("THEN '!'", "THEN ''"),
		"unicode coordinate guard":             replace("octet_length(package_name) <> length(package_name)", "false"),
		"unsafe top-level key guard":           replace("octet_length(key) <> length(key)", "false"),
		"resolved field alias guard":           replace("'resolvedpackages'", "'otherfield'"),
		"fastupdate enabled":                   replace("fastupdate=off", "fastupdate=on"),
		"fastupdate default restored":          replace(" WITH (fastupdate=off)", ""),
		"function no longer immutable":         replace("LANGUAGE SQL IMMUTABLE", "LANGUAGE SQL VOLATILE"),
		"coordinate body changed":              replace("SELECT substr(raw, 5)", "SELECT substr(raw, 4)"),
		"JSON array guard changed":             replace("jsonb_typeof(packages) = 'array'", "true"),
		"function helper renamed":              replace("CREATE FUNCTION csx_builder_coord(", "CREATE FUNCTION unexpected_helper("),
		"case folding changed":                 replace("'ABCDEFGHIJKLMNOPQRSTUVWXYZ'", "'abcdefghijklmnopqrstuvwxyZ'"),
		"body block-comment marker in literal": replace("'pkg:'", "'pkg:/*literal data*/'"),
		"body line-comment marker in literal":  replace("'pkg:'", "'pkg:--literal data'"),
		"function body comment added":          replace("AS $coord$\n", "AS $coord$\n-- changed reviewed body\n"),
		"missing changed-evidence index": replace(
			"CREATE INDEX builder_evidence_changed_idx ON evidence_agg(last_seen, purl, symbol);", ""),
		"additional additive SQL":    sql + "\nCREATE TABLE unreviewed(id BIGINT);",
		"additional destructive SQL": sql + "\nDELETE FROM receipts;",
		"duplicate migration":        sql + sql,
		"comment edited":             sql + "-- changed reviewed file\n",
		"outer whitespace edited":    sql + " ",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if body == sql {
				t.Fatal("mutation did not change the reviewed migration")
			}
			if err := ValidateMigrationSQL("0036_builder_scope_indexes.sql", body); err == nil {
				t.Fatal("changed builder scope migration passed its pinned digest")
			}
		})
	}
}
