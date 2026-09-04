package deploygate

import (
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

func ValidateMigrationSQL(name, sql string) error {
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("migration %s is empty", name)
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
