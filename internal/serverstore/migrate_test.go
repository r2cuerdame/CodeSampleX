package serverstore

import (
	"sort"
	"strings"
	"testing"
)

func TestLoadMigrations(t *testing.T) {
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("no migrations embedded")
	}
	if migs[0].Version != "0001_init.sql" {
		t.Fatalf("first migration = %q, want 0001_init.sql", migs[0].Version)
	}
	versions := make([]string, len(migs))
	for i, m := range migs {
		versions[i] = m.Version
		if len(m.Statements) == 0 {
			t.Errorf("%s: no statements after split", m.Version)
		}
		for j, s := range m.Statements {
			if strings.TrimSpace(s) == "" {
				t.Errorf("%s: statement %d is empty", m.Version, j)
			}
			if strings.Contains(s, ";") {
				t.Errorf("%s: statement %d still contains a semicolon: %q", m.Version, j, s)
			}
		}
	}
	if !sort.StringsAreSorted(versions) {
		t.Errorf("migrations not ordered by version: %v", versions)
	}
}

func TestInitMigrationCoversC4Schema(t *testing.T) {
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	all := strings.Join(migs[0].Statements, "\n")
	for _, table := range []string{
		"packages", "symbols", "evidence_agg", "evidence_dedup", "cases",
		"samples", "receipts", "compatibility_snapshots", "failure_clusters",
		"identities", "verification_jobs", "peers", "shards", "stats_daily",
	} {
		if !strings.Contains(all, "CREATE TABLE "+table+"(") {
			t.Errorf("0001_init.sql missing CREATE TABLE %s", table)
		}
	}
	// The C4 amendment: evidence_dedup records the bucket's last-contributed
	// count so re-sent batches merge as deltas.
	dedup := ""
	for _, s := range migs[0].Statements {
		if strings.Contains(s, "CREATE TABLE evidence_dedup(") {
			dedup = s
		}
	}
	if dedup == "" {
		t.Fatal("evidence_dedup statement not found")
	}
	if !strings.Contains(dedup, "count BIGINT NOT NULL DEFAULT 0") {
		t.Errorf("evidence_dedup lacks amended count column:\n%s", dedup)
	}
	if !strings.Contains(dedup, "PRIMARY KEY(bucket_kind,bucket,agg_id,epoch)") {
		t.Errorf("evidence_dedup lacks C4 primary key:\n%s", dedup)
	}
}

func TestSplitStatements(t *testing.T) {
	in := "-- leading comment; with semicolon\nCREATE TABLE a(x INT); \n\n-- another\nCREATE INDEX b ON a(x);\n"
	got := splitStatements(in)
	if len(got) != 2 {
		t.Fatalf("splitStatements returned %d statements, want 2: %#v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "CREATE TABLE a") {
		t.Errorf("statement 0 = %q", got[0])
	}
	if !strings.HasPrefix(got[1], "CREATE INDEX b") {
		t.Errorf("statement 1 = %q", got[1])
	}
}

func TestActivityMigrationIsAdditiveAndPrivacyBounded(t *testing.T) {
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	last := migs[len(migs)-1]
	if last.Version != "0008_activity_buckets.sql" {
		t.Fatalf("last migration = %q", last.Version)
	}
	all := strings.ToLower(strings.Join(last.Statements, "\n"))
	for _, required := range []string{"create table if not exists activity_buckets", "create table if not exists activity_health", "octet_length(bucket) = 16", "kind", "epoch", "owner", "first_seen", "last_seen"} {
		if !strings.Contains(all, required) {
			t.Errorf("activity migration missing %q", required)
		}
	}
	raw, err := migrationsFS.ReadFile("migrations/0008_activity_buckets.sql")
	if err != nil {
		t.Fatal(err)
	}
	comments := strings.ToLower(string(raw))
	for _, required := range []string{"privacy boundary", "ipv4 space is enumerable", "keyed pseudonyms"} {
		if !strings.Contains(comments, required) {
			t.Errorf("activity migration privacy disclosure missing %q", required)
		}
	}
	for _, forbidden := range []string{"ip_address", "remote_addr", "forwarded_for", "user_agent", "route_param"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("activity migration contains PII column %q", forbidden)
		}
	}
}
