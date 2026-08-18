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
	var activityMigration Migration
	for _, migration := range migs {
		if migration.Version == "0008_activity_buckets.sql" {
			activityMigration = migration
			break
		}
	}
	if activityMigration.Version == "" {
		t.Fatal("0008_activity_buckets.sql not loaded")
	}
	all := strings.ToLower(strings.Join(activityMigration.Statements, "\n"))
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

func TestSearchOutcomeMigrationStoresOnlyDailyAggregates(t *testing.T) {
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var search Migration
	for _, migration := range migs {
		if migration.Version == "0009_search_outcomes.sql" {
			search = migration
			break
		}
	}
	if search.Version == "" {
		t.Fatal("0009_search_outcomes.sql not loaded")
	}
	all := strings.ToLower(strings.Join(search.Statements, "\n"))
	for _, required := range []string{"create table if not exists search_outcomes_daily", "day", "sample_hits", "no_matches", "updated_at"} {
		if !strings.Contains(all, required) {
			t.Errorf("search outcome migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"query", "package", "symbol", "path", "user", "bucket", "client", "request_id", "ip_address", "user_agent"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("search outcome schema contains identifying/raw field %q", forbidden)
		}
	}
}

func TestLegacyMatrixRetirementIsNondestructiveAndExact(t *testing.T) {
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var matrix Migration
	for _, migration := range migs {
		if migration.Version == "0010_retire_unpreparable_matrix_jobs.sql" {
			matrix = migration
			break
		}
	}
	if matrix.Version == "" {
		t.Fatal("0010_retire_unpreparable_matrix_jobs.sql not loaded")
	}
	last := strings.ToLower(strings.Join(matrix.Statements, "\n"))
	for _, required := range []string{
		"update verification_jobs", "reason = 'matrix'", "status in ('open', 'claimed')",
		"set status = 'done'", "runtimeversion", "maven-java@1", "gradle-java@1",
		"is distinct from 'container_run'", "<> '{}'::jsonb",
	} {
		if !strings.Contains(last, required) {
			t.Errorf("matrix retirement migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"delete from verification_jobs", "truncate", "drop table"} {
		if strings.Contains(last, forbidden) {
			t.Errorf("matrix retirement migration contains destructive operation %q", forbidden)
		}
	}
}

func TestAuthoringSessionMigrationStoresOnlyHashedPrivateCapabilities(t *testing.T) {
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var authoring Migration
	for _, migration := range migs {
		if migration.Version == "0011_authoring_sessions.sql" {
			authoring = migration
			break
		}
	}
	if authoring.Version == "" {
		t.Fatal("0011_authoring_sessions.sql not loaded")
	}
	all := strings.ToLower(strings.Join(authoring.Statements, "\n"))
	for _, required := range []string{"token_hash", "session_id", "idle_expires_at", "revoked_at", "last_refresh_ip", "computer_name"} {
		if !strings.Contains(all, required) {
			t.Errorf("authoring migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"raw_token", "bearer_token", "prompt", "source", "project_path"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("authoring migration stores forbidden field %q", forbidden)
		}
	}
}

func TestAuthoringDraftMigrationStaysOutsidePublicSamples(t *testing.T) {
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var draft Migration
	for _, migration := range migs {
		if migration.Version == "0012_authoring_drafts.sql" {
			draft = migration
			break
		}
	}
	if draft.Version == "" {
		t.Fatal("0012_authoring_drafts.sql not loaded")
	}
	all := strings.ToLower(strings.Join(draft.Statements, "\n"))
	for _, required := range []string{"create table authoring_drafts", "create table authoring_assignments", "sample_id", "session_id", "worker_label", "manifest", "local_status", "lease_expires_at", "live_session"} {
		if !strings.Contains(all, required) {
			t.Errorf("authoring draft migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"insert into samples", "verification_jobs", "raw_token", "bearer_token"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("authoring draft migration crosses private boundary with %q", forbidden)
		}
	}
}

func TestAuthoringExpansionMigrationAddsBoundedWorkKinds(t *testing.T) {
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var expansion Migration
	for _, migration := range migs {
		if migration.Version == "0013_authoring_expansion.sql" {
			expansion = migration
			break
		}
	}
	if expansion.Version == "" {
		t.Fatal("0013_authoring_expansion.sql not loaded")
	}
	all := strings.ToLower(strings.Join(expansion.Statements, "\n"))
	for _, required := range []string{"alter table authoring_assignments", "kind", "score", "wanted", "finding", "expansion"} {
		if !strings.Contains(all, required) {
			t.Errorf("authoring expansion migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"delete from", "insert into samples", "raw_token", "bearer_token"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("authoring expansion migration crosses boundary with %q", forbidden)
		}
	}
}
