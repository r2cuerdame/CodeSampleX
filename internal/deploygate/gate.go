package deploygate

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

type Request struct {
	MergeVerdict          string
	RequiresHumanDecision string
	SideEffectClass       string
}

func ValidateDecision(req Request) error {
	if req.MergeVerdict != "pass" {
		return fmt.Errorf("merge verdict is %q, want pass", req.MergeVerdict)
	}
	if req.RequiresHumanDecision != "no" {
		return fmt.Errorf("requires_human_decision is %q, want no", req.RequiresHumanDecision)
	}
	switch req.SideEffectClass {
	case "safe", "additive-migration":
		return nil
	default:
		return fmt.Errorf("side effect class %q is not eligible for automatic deploy", req.SideEffectClass)
	}
}

func ValidateMigrationClass(class string, migrations []string) error {
	hasMigration := len(migrations) != 0
	if hasMigration && class != "additive-migration" {
		return fmt.Errorf("%d migration(s) changed but side effect class is %q", len(migrations), class)
	}
	if !hasMigration && class == "additive-migration" {
		return fmt.Errorf("additive-migration was declared but no migration was added")
	}
	return nil
}

var (
	lineComment          = regexp.MustCompile(`(?m)--[^\n]*`)
	blockComment         = regexp.MustCompile(`(?s)/\*.*?\*/`)
	addColumnStatement   = regexp.MustCompile(`(?is)^alter\s+table\s+[a-z_][a-z0-9_]*\s+add\s+column\s+[a-z_][a-z0-9_]*\s+.+$`)
	createTableStatement = regexp.MustCompile(`(?is)^create\s+table\s+([a-z_][a-z0-9_]*)\s*\((.*)\)$`)
	additiveColumn       = regexp.MustCompile(`(?is)^[a-z_][a-z0-9_]*\s+(?:bigserial|bigint|text|jsonb|timestamptz)(?:\s+(?:primary\s+key|not\s+null|unique|default\s+(?:'(?:[^']|'')*'|-?[0-9]+|true|false|now\(\))))*$`)
	createIndexStatement = regexp.MustCompile(`(?is)^create\s+index\s+[a-z_][a-z0-9_]*\s+on\s+([a-z_][a-z0-9_]*)\s*\(\s*[a-z_][a-z0-9_]*(?:\s*,\s*[a-z_][a-z0-9_]*)*\s*\)$`)
	r2c152Backfill       = regexp.MustCompile(`(?is)^update\s+evidence_agg\s+set\s+evidence_quality\s*=\s*''\s+where\s+result\s*=\s*'PASS'$`)
)

var samplePackageProjectionStatements = []string{
	`CREATE TABLE sample_packages(
  sample_id TEXT NOT NULL REFERENCES samples(sample_id) ON DELETE CASCADE,
  purl TEXT NOT NULL,
  coord TEXT NOT NULL,
  PRIMARY KEY(sample_id, purl))`,
	`CREATE INDEX sample_packages_coord_idx ON sample_packages(coord, sample_id)`,
	`INSERT INTO sample_packages(sample_id, purl, coord)
SELECT s.sample_id,
       package.value,
       left(package.value,
            length(package.value) - strpos(reverse(package.value), '@') + 1)
  FROM samples s
  CROSS JOIN LATERAL jsonb_array_elements_text(
    CASE WHEN jsonb_typeof(s.manifest->'packages') = 'array'
         THEN s.manifest->'packages' ELSE '[]'::jsonb END
  ) AS package(value)
 WHERE strpos(reverse(package.value), '@') > 0
ON CONFLICT DO NOTHING`,
}

var dependencyEdgeParentIdxStatements = []string{
	`CREATE INDEX IF NOT EXISTS dependency_edge_parent_idx ON dependency_edge (ecosystem, parent_name)`,
}

var evidenceAggDirectIdxStatements = []string{
	`CREATE INDEX IF NOT EXISTS evidence_agg_direct_purl_idx ON evidence_agg (purl) WHERE direct`,
}

var samplesManifestTrgmIdxStatements = []string{
	`CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public`,
	`CREATE INDEX IF NOT EXISTS samples_manifest_lower_trgm_idx ON samples USING gin ((lower(manifest::text)) public.gin_trgm_ops) WHERE NOT quarantined`,
}

var wantedDedupEpochCoordinateIdxStatements = []string{
	`CREATE INDEX IF NOT EXISTS wanted_dedup_epoch_coordinate_idx ON wanted_dedup(epoch DESC, ecosystem, name, version, symbol, target_os)`,
}

var slowQueryIndexesStatements = []string{
	`CREATE INDEX IF NOT EXISTS failure_clusters_pkg_count_idx ON failure_clusters (package_name, observation_count DESC, id)`,
	`CREATE INDEX IF NOT EXISTS samples_live_created_id_idx ON samples (created_at DESC, sample_id) WHERE NOT quarantined`,
}

// #433 adds one partial index to an existing hot table. The production
// migration path is offline and quiescent, so admit only this reviewed shape;
// the general grammar must not learn indexes on existing tables.
var failureClusterPageIdxStatements = []string{
	`CREATE INDEX IF NOT EXISTS failure_clusters_current_page_idx
  ON failure_clusters (ecosystem, package_name, observation_count DESC, id)
  WHERE (
    COALESCE(evidence_quality, 'legacy-evidence-incomplete') NOT IN
      ('missing', 'legacy-evidence-incomplete')
    OR COALESCE(error_fp, '') = ''
  )`,
}

// #383 adds only a new presence table and indexes on that table. Its named
// composite constraint and IF NOT EXISTS are outside the generic grammar;
// admit this exact artifact without broadening that grammar.
var activeInstallationsStatements = []string{
	`CREATE TABLE IF NOT EXISTS active_installations (
  id BIGSERIAL PRIMARY KEY,
  interval_kind TEXT NOT NULL,
  epoch TEXT NOT NULL,
  token TEXT NOT NULL,
  client_class TEXT NOT NULL,
  client_version TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT active_installations_token_key UNIQUE (interval_kind, epoch, token)
)`,
	`CREATE INDEX IF NOT EXISTS active_installations_count_idx
ON active_installations (interval_kind, epoch, client_class)`,
	`CREATE INDEX IF NOT EXISTS active_installations_prune_idx
ON active_installations (updated_at)`,
}

// This migration creates only anonymous analytics tables and their indexes,
// plus the collection-start singleton. Pin its checks, foreign key and insert
// to this reviewed artifact without expanding the generic SQL grammar.
var anonymousAnalyticsStatements = []string{
	`CREATE TABLE anonymous_clients (
  client_hash TEXT PRIMARY KEY CHECK (client_hash ~ '^[0-9a-f]{64}$'),
  first_seen TIMESTAMPTZ NOT NULL,
  last_seen TIMESTAMPTZ NOT NULL,
  request_count BIGINT NOT NULL CHECK (request_count > 0)
)`,
	`CREATE INDEX anonymous_clients_first_seen_idx ON anonymous_clients (first_seen)`,
	`CREATE INDEX anonymous_clients_last_seen_idx ON anonymous_clients (last_seen)`,
	`CREATE TABLE anonymous_client_days (
  day DATE NOT NULL,
  client_hash TEXT NOT NULL REFERENCES anonymous_clients(client_hash) ON DELETE CASCADE,
  request_count BIGINT NOT NULL CHECK (request_count > 0),
  PRIMARY KEY (day, client_hash)
)`,
	`CREATE INDEX anonymous_client_days_client_idx ON anonymous_client_days (client_hash, day)`,
	`CREATE TABLE anonymous_analytics_collection (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  started_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`,
	`INSERT INTO anonymous_analytics_collection(singleton) VALUES (TRUE)`,
}

var anonymousCredentialAdoptionStatements = []string{
	`ALTER TABLE anonymous_client_days ADD COLUMN credential_present_count BIGINT NOT NULL DEFAULT 0 CHECK (credential_present_count >= 0)`,
	`ALTER TABLE anonymous_client_days ADD COLUMN credential_issued_count BIGINT NOT NULL DEFAULT 0 CHECK (credential_issued_count >= 0)`,
	`ALTER TABLE anonymous_analytics_collection ADD COLUMN credential_adoption_started_at TIMESTAMPTZ NOT NULL DEFAULT now()`,
}

var farmCoverageStatements = []string{
	`CREATE TABLE farm_coverage(
  os TEXT NOT NULL,
  ecosystem TEXT NOT NULL,
  observed INT NOT NULL DEFAULT 0,
  measured INT NOT NULL DEFAULT 0,
  proven INT NOT NULL DEFAULT 0,
  observed_proven INT NOT NULL DEFAULT 0,
  PRIMARY KEY(os, ecosystem))`,
	`CREATE TABLE farm_coverage_meta(
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  generated_at TIMESTAMPTZ NOT NULL)`,
}

// cliWorkKindStatements is 0049: the assignment kind vocabulary widened to
// admit CLI work (#81), exactly as 0022 widened it for DEPENDENCY. Pinned as
// its two statements: the general allowlist must not learn DROP CONSTRAINT
// or CHECK rewrites, and a changed vocabulary requires a fresh review.
var cliWorkKindStatements = []string{
	`ALTER TABLE authoring_assignments
  DROP CONSTRAINT IF EXISTS authoring_assignments_kind_check`,
	`ALTER TABLE authoring_assignments
  ADD CONSTRAINT authoring_assignments_kind_check
    CHECK (kind IN ('WANTED','FINDING','EXPANSION','DEPENDENCY','CLI'))`,
}

func ValidateMigrationSQL(name, sql string) error {
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("migration %s is empty", name)
	}

	// Pin this reviewed additive migration as a whole, including its SQL
	// function and index settings. Only platform line endings may differ.
	// The general grammar must never learn arbitrary functions or expression
	// indexes from this exception. A changed body requires a fresh review.
	if name == "0036_builder_projections.sql" {
		const reviewedSHA256 = "3499206df74ee5cbf2ec8644de7e9028899061440107782a4e21f6faf3e89133"
		digest := sha256.Sum256([]byte(strings.ReplaceAll(sql, "\r\n", "\n")))
		if fmt.Sprintf("%x", digest) != reviewedSHA256 {
			return fmt.Errorf("migration %s does not match the reviewed builder projection SHA256", name)
		}
		return nil
	}

	// Automatic production migration is an allowlist, not a blacklist. This
	// intentionally accepts the R2C-152 fixture (ADD COLUMN plus its bounded
	// evidence-quality backfill), and isolated new tables with simple indexes.
	// Indexes are eligible only when their table was created earlier in this
	// same migration, so this path cannot add load or locks to an existing
	// production table. Splitting first prevents a harmless first statement
	// from hiding a destructive second statement on the same line.
	clean := blockComment.ReplaceAllString(lineComment.ReplaceAllString(sql, ""), "")
	var statements []string
	for _, raw := range strings.Split(clean, ";") {
		if statement := strings.TrimSpace(raw); statement != "" {
			statements = append(statements, statement)
		}
	}
	if len(statements) == 0 {
		return fmt.Errorf("migration %s contains no SQL statements", name)
	}
	// This migration deliberately reads an existing table to populate a new
	// projection. Approve only its exact three statements, in order. That keeps
	// the general allowlist from learning arbitrary FKs or INSERT...SELECT and
	// preserves fail-closed behavior if any source, target, constraint, index,
	// conflict guard, or fourth statement changes.
	if name == "0028_sample_packages.sql" {
		if exactStatements(statements, samplePackageProjectionStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact automatic projection allowlist", name)
	}
	if name == "0032_dependency_edge_parent_idx.sql" {
		if exactStatements(statements, dependencyEdgeParentIdxStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact dependency edge index allowlist", name)
	}
	if name == "0033_evidence_agg_direct_idx.sql" {
		if exactStatements(statements, evidenceAggDirectIdxStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact evidence agg direct index allowlist", name)
	}
	if name == "0034_samples_manifest_trgm_idx.sql" {
		if exactStatements(statements, samplesManifestTrgmIdxStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact samples manifest trgm index allowlist", name)
	}
	if name == "0035_recent_wanted_demand.sql" {
		if exactStatements(statements, wantedDedupEpochCoordinateIdxStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact wanted demand index allowlist", name)
	}
	if name == "0037_slow_query_indexes.sql" {
		if exactStatements(statements, slowQueryIndexesStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact slow query indexes allowlist", name)
	}
	if name == "0038_active_installations.sql" {
		if exactStatements(statements, activeInstallationsStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact active installations allowlist", name)
	}

	if name == "0040_anonymous_analytics.sql" {
		if exactStatements(statements, anonymousAnalyticsStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact anonymous analytics allowlist", name)
	}
	if name == "0041_anonymous_credential_adoption.sql" {
		if exactStatements(statements, anonymousCredentialAdoptionStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact anonymous credential adoption allowlist", name)
	}
	if name == "0042_failure_cluster_page_idx.sql" {
		if exactStatements(statements, failureClusterPageIdxStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact failure cluster page index allowlist", name)
	}
	if name == "0045_farm_coverage.sql" {
		if exactStatements(statements, farmCoverageStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact farm coverage allowlist", name)
	}
	if name == "0049_cli_work_kind.sql" {
		if exactStatements(statements, cliWorkKindStatements) {
			return nil
		}
		return fmt.Errorf("migration %s does not match the exact CLI work kind allowlist", name)
	}

	createdTables := make(map[string]bool)
	for _, statement := range statements {
		if addColumnStatement.MatchString(statement) || r2c152Backfill.MatchString(statement) {
			continue
		}
		if table, ok := additiveCreatedTable(statement); ok {
			createdTables[table] = true
			continue
		}
		if match := createIndexStatement.FindStringSubmatch(statement); match != nil && createdTables[strings.ToLower(match[1])] {
			continue
		}
		return fmt.Errorf("migration %s contains a statement outside the automatic additive allowlist: %s", name, statement)
	}
	return nil
}

func exactStatements(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if normalizeStatement(got[i]) != normalizeStatement(want[i]) {
			return false
		}
	}
	return true
}

func normalizeStatement(statement string) string {
	return strings.Join(strings.Fields(statement), " ")
}

func additiveCreatedTable(statement string) (string, bool) {
	match := createTableStatement.FindStringSubmatch(statement)
	if match == nil {
		return "", false
	}
	for _, raw := range strings.Split(match[2], ",") {
		if !additiveColumn.MatchString(strings.TrimSpace(raw)) {
			return "", false
		}
	}
	return strings.ToLower(match[1]), true
}
