package deploygate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEligibilityRequiresProjectOpsPassAndNoHumanGate(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  Request
	}{
		{"merge hold", Request{MergeVerdict: "hold", RequiresHumanDecision: "no", SideEffectClass: "safe"}},
		{"human decision", Request{MergeVerdict: "pass", RequiresHumanDecision: "yes", SideEffectClass: "safe"}},
		{"manual class", Request{MergeVerdict: "pass", RequiresHumanDecision: "no", SideEffectClass: "manual"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateDecision(tc.req); err == nil {
				t.Fatal("unsafe ProjectOps decision was accepted")
			}
		})
	}
	if err := ValidateDecision(Request{MergeVerdict: "pass", RequiresHumanDecision: "no", SideEffectClass: "safe"}); err != nil {
		t.Fatalf("safe decision rejected: %v", err)
	}
}

func TestMigrationPolicyAllowsR2C152AndRejectsDestructiveSQL(t *testing.T) {
	additive := `ALTER TABLE evidence_agg ADD COLUMN termination_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_agg ADD COLUMN exit_code INTEGER;
ALTER TABLE evidence_agg ADD COLUMN signal TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_agg ADD COLUMN timeout_millis BIGINT NOT NULL DEFAULT 0;
ALTER TABLE evidence_agg ADD COLUMN error_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_agg ADD COLUMN evidence_quality TEXT NOT NULL DEFAULT 'legacy-evidence-incomplete';
UPDATE evidence_agg SET evidence_quality = '' WHERE result = 'PASS';
ALTER TABLE failure_clusters ADD COLUMN termination_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE failure_clusters ADD COLUMN exit_code INTEGER;
ALTER TABLE failure_clusters ADD COLUMN signal TEXT NOT NULL DEFAULT '';
ALTER TABLE failure_clusters ADD COLUMN timeout_millis BIGINT NOT NULL DEFAULT 0;
ALTER TABLE failure_clusters ADD COLUMN error_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE failure_clusters ADD COLUMN evidence_quality TEXT NOT NULL DEFAULT 'legacy-evidence-incomplete';
ALTER TABLE failure_clusters ADD COLUMN env_variants JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE failure_clusters ADD COLUMN evidence_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE failure_clusters ADD COLUMN diagnostic_candidate BOOLEAN NOT NULL DEFAULT false;`
	if err := ValidateMigrationSQL("0024_failure_evidence.sql", additive); err != nil {
		t.Fatalf("R2C-152-style additive migration rejected: %v", err)
	}
	for name, sql := range map[string]string{
		"drop":               "DROP TABLE evidence_agg;",
		"truncate":           "TRUNCATE evidence_agg;",
		"delete":             "DELETE FROM evidence_agg;",
		"rename":             "ALTER TABLE evidence_agg RENAME TO old_evidence;",
		"type":               "ALTER TABLE evidence_agg ALTER COLUMN stage TYPE integer;",
		"grant":              "GRANT ALL ON evidence_agg TO public;",
		"same-line bypass":   "ALTER TABLE evidence_agg ADD COLUMN harmless TEXT; DROP TABLE samples;",
		"unbounded backfill": "UPDATE evidence_agg SET evidence_quality = '';",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateMigrationSQL("0099_bad.sql", sql); err == nil {
				t.Fatalf("destructive/sensitive migration accepted: %s", sql)
			}
		})
	}
}

func migrationSQL(t *testing.T, name string) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate deploygate test file")
	}
	migrationPath := filepath.Join(filepath.Dir(testFile), "..", "serverstore", "migrations", name)
	raw, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

func TestFailureStageLineageMigrationIsAutomaticAdditive(t *testing.T) {
	const name = "0025_failure_stage_lineage.sql"
	if err := ValidateMigrationSQL(name, migrationSQL(t, name)); err != nil {
		t.Fatalf("failure-stage lineage migration rejected: %v", err)
	}
}

func TestIsolatedTableMigrationsAreAutomaticAdditive(t *testing.T) {
	for _, name := range []string{"0026_anomaly_reports.sql", "0027_csx_issue_reports.sql"} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateMigrationSQL(name, migrationSQL(t, name)); err != nil {
				t.Fatalf("isolated additive table migration rejected: %v", err)
			}
		})
	}
}

func TestSamplePackageProjectionMigrationIsAutomaticAdditive(t *testing.T) {
	const name = "0028_sample_packages.sql"
	if err := ValidateMigrationSQL(name, migrationSQL(t, name)); err != nil {
		t.Fatalf("sample-package projection migration rejected: %v", err)
	}
}

func TestEvidenceAggDirectIdxMigrationIsAutomaticAdditive(t *testing.T) {
	const name = "0033_evidence_agg_direct_idx.sql"
	if err := ValidateMigrationSQL(name, migrationSQL(t, name)); err != nil {
		t.Fatalf("evidence_agg direct index migration rejected: %v", err)
	}
}

func TestSamplesManifestTrgmIdxMigrationIsAutomaticAdditive(t *testing.T) {
	const name = "0034_samples_manifest_trgm_idx.sql"
	if err := ValidateMigrationSQL(name, migrationSQL(t, name)); err != nil {
		t.Fatalf("samples manifest trgm index migration rejected: %v", err)
	}
}

func TestRecentWantedDemandMigrationIsAutomaticAdditive(t *testing.T) {
	const name = "0035_recent_wanted_demand.sql"
	if err := ValidateMigrationSQL(name, migrationSQL(t, name)); err != nil {
		t.Fatalf("recent wanted demand migration rejected: %v", err)
	}
}

func TestRecentWantedDemandIndexExceptionRemainsFailClosed(t *testing.T) {
	valid := wantedDedupEpochCoordinateIdxStatements[0] + ";"
	for name, sql := range map[string]string{
		"wrong filename":       valid,
		"wrong index name":     strings.Replace(valid, "wanted_dedup_epoch_coordinate_idx", "wanted_dedup_recent_idx", 1),
		"wrong table":          strings.Replace(valid, "ON wanted_dedup", "ON wanted", 1),
		"missing idempotence":  strings.Replace(valid, " IF NOT EXISTS", "", 1),
		"missing descending":   strings.Replace(valid, "epoch DESC", "epoch", 1),
		"reordered coordinate": strings.Replace(valid, "ecosystem, name", "name, ecosystem", 1),
		"missing coordinate":   strings.Replace(valid, ", target_os", "", 1),
		"identity column":      strings.Replace(valid, ", target_os", ", target_os, anon_id", 1),
		"duplicate statement":  valid + "\n" + valid,
		"drop suffix":          valid + "\nDROP TABLE wanted_dedup;",
		"add-column suffix":    valid + "\nALTER TABLE wanted_dedup ADD COLUMN unsafe TEXT;",
	} {
		t.Run(name, func(t *testing.T) {
			migrationName := "0035_recent_wanted_demand.sql"
			if name == "wrong filename" {
				migrationName = "0099_recent_wanted_demand.sql"
			}
			if err := ValidateMigrationSQL(migrationName, sql); err == nil {
				t.Fatalf("changed wanted demand index migration accepted: %s", sql)
			}
		})
	}
}

func TestSlowQueryIndexesMigrationIsAutomaticAdditive(t *testing.T) {
	const name = "0037_slow_query_indexes.sql"
	if err := ValidateMigrationSQL(name, migrationSQL(t, name)); err != nil {
		t.Fatalf("slow query indexes migration rejected: %v", err)
	}
}

func TestSlowQueryIndexesExceptionRemainsFailClosed(t *testing.T) {
	valid := strings.Join(slowQueryIndexesStatements, ";\n") + ";"
	for name, sql := range map[string]string{
		"wrong filename":      valid,
		"wrong index name":    strings.Replace(valid, "failure_clusters_pkg_count_idx", "failure_clusters_wrong_idx", 1),
		"wrong table":         strings.Replace(valid, "ON failure_clusters", "ON failure_evidence", 1),
		"missing idempotence": strings.Replace(valid, " IF NOT EXISTS", "", 1),
		"missing statement":   slowQueryIndexesStatements[0] + ";",
		"drop suffix":         valid + "\nDROP TABLE failure_clusters;",
		"add-column suffix":   valid + "\nALTER TABLE samples ADD COLUMN unsafe TEXT;",
	} {
		t.Run(name, func(t *testing.T) {
			migrationName := "0037_slow_query_indexes.sql"
			if name == "wrong filename" {
				migrationName = "0099_slow_query_indexes.sql"
			}
			if err := ValidateMigrationSQL(migrationName, sql); err == nil {
				t.Fatalf("changed slow query index migration accepted: %s", sql)
			}
		})
	}
}

func TestAuthoringWorkAxisMigrationIsAutomaticAdditive(t *testing.T) {
	const name = "0034_authoring_work_axis.sql"
	if err := ValidateMigrationSQL(name, migrationSQL(t, name)); err != nil {
		t.Fatalf("authoring work axis migration rejected: %v", err)
	}
}

func TestSamplePackageProjectionExceptionRemainsFailClosed(t *testing.T) {
	valid := migrationSQL(t, "0028_sample_packages.sql")
	withoutStatement := func(i int) string {
		return strings.Replace(valid, samplePackageProjectionStatements[i]+";", "", 1)
	}

	for name, sql := range map[string]string{
		"wrong filename":           valid,
		"missing table":            withoutStatement(0),
		"missing index":            withoutStatement(1),
		"missing backfill":         withoutStatement(2),
		"wrong parent":             strings.Replace(valid, "REFERENCES samples(sample_id)", "REFERENCES receipts(receipt_id)", 1),
		"missing cascade":          strings.Replace(valid, " ON DELETE CASCADE", "", 1),
		"wrong index":              strings.Replace(valid, "sample_packages(coord, sample_id)", "sample_packages(purl, sample_id)", 1),
		"wrong insert target":      strings.Replace(valid, "INSERT INTO sample_packages", "INSERT INTO receipts", 1),
		"wrong source":             strings.Replace(valid, "FROM samples s", "FROM receipts s", 1),
		"missing conflict guard":   strings.Replace(valid, "ON CONFLICT DO NOTHING;", "", 1),
		"changed JSON literal":     strings.Replace(valid, "'packages'", "'PACKAGES'", 1),
		"duplicate backfill":       valid + "\n" + samplePackageProjectionStatements[2] + ";",
		"extra fourth statement":   valid + "\nCREATE TABLE harmless(id BIGINT);",
		"drop suffix":              valid + "\nDROP TABLE samples;",
		"truncate suffix":          valid + "\nTRUNCATE samples;",
		"delete suffix":            valid + "\nDELETE FROM samples;",
		"update suffix":            valid + "\nUPDATE samples SET status='gone';",
		"add-column suffix":        valid + "\nALTER TABLE samples ADD COLUMN unsafe TEXT;",
		"destructive alter suffix": valid + "\nALTER TABLE samples DROP COLUMN manifest;",
	} {
		t.Run(name, func(t *testing.T) {
			migrationName := "0028_sample_packages.sql"
			if name == "wrong filename" {
				migrationName = "0099_sample_packages.sql"
			}
			if err := ValidateMigrationSQL(migrationName, sql); err == nil {
				t.Fatalf("unsafe sample-package migration accepted: %s", sql)
			}
		})
	}
	if err := ValidateMigrationSQL("0099_arbitrary.sql",
		"INSERT INTO sample_packages SELECT sample_id, '', '' FROM samples;"); err == nil {
		t.Fatal("arbitrary INSERT...SELECT was accepted")
	}
}

func TestIsolatedTableAllowlistDoesNotTouchExistingObjects(t *testing.T) {
	if err := ValidateMigrationSQL("0098_new.sql", `
CREATE TABLE new_reports(
  id BIGSERIAL PRIMARY KEY,
  payload JSONB NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX new_reports_status_idx ON new_reports(status, created_at);`); err != nil {
		t.Fatalf("strict isolated table and index rejected: %v", err)
	}

	for name, sql := range map[string]string{
		"copy existing data":          "CREATE TABLE copied AS SELECT * FROM evidence_agg;",
		"index existing table":        "CREATE INDEX evidence_stage_idx ON evidence_agg(stage);",
		"cross-table reference":       "CREATE TABLE new_reports(id BIGINT REFERENCES evidence_agg(id));",
		"unapproved default":          "CREATE TABLE new_reports(created_at TIMESTAMPTZ DEFAULT clock_timestamp());",
		"destructive second stmt":     "CREATE TABLE new_reports(id BIGINT); DROP TABLE evidence_agg;",
		"existing index after create": "CREATE TABLE new_reports(id BIGINT); CREATE INDEX evidence_stage_idx ON evidence_agg(stage);",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateMigrationSQL("0099_bad.sql", sql); err == nil {
				t.Fatalf("non-isolated create migration accepted: %s", sql)
			}
		})
	}
}

func TestR2C152MigrationFilePassesAutomaticGate(t *testing.T) {
	const name = "0024_failure_evidence.sql"
	if err := ValidateMigrationSQL(name, migrationSQL(t, name)); err != nil {
		t.Fatalf("production migration is not eligible for unattended additive rollout: %v", err)
	}
}

func TestMigrationPresenceMustMatchTheDeclaredSideEffectClass(t *testing.T) {
	if err := ValidateMigrationClass("safe", []string{"0024_failure_evidence.sql"}); err == nil {
		t.Fatal("migration hidden behind safe class")
	}
	if err := ValidateMigrationClass("additive-migration", nil); err == nil {
		t.Fatal("additive-migration declared without a migration")
	}
	if err := ValidateMigrationClass("additive-migration", []string{"0024_failure_evidence.sql"}); err != nil {
		t.Fatalf("declared additive migration rejected: %v", err)
	}
}
