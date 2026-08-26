package deploygate

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestFailureStageLineageMigrationIsAutomaticAdditive(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate deploygate test file")
	}
	migrationPath := filepath.Join(filepath.Dir(testFile), "..", "serverstore", "migrations", "0025_failure_stage_lineage.sql")
	sql, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read failure-stage lineage migration: %v", err)
	}
	if err := ValidateMigrationSQL(filepath.Base(migrationPath), string(sql)); err != nil {
		t.Fatalf("failure-stage lineage migration rejected: %v", err)
	}
}

func TestR2C152MigrationFilePassesAutomaticGate(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate deploygate test file")
	}
	migrationPath := filepath.Join(filepath.Dir(testFile), "..", "serverstore", "migrations", "0024_failure_evidence.sql")
	sql, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read production migration: %v", err)
	}
	if err := ValidateMigrationSQL(filepath.Base(migrationPath), string(sql)); err != nil {
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
