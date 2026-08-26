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
	lineComment        = regexp.MustCompile(`(?m)--[^\n]*`)
	blockComment       = regexp.MustCompile(`(?s)/\*.*?\*/`)
	addColumnStatement = regexp.MustCompile(`(?is)^alter\s+table\s+[a-z_][a-z0-9_]*\s+add\s+column\s+[a-z_][a-z0-9_]*\s+.+$`)
	r2c152Backfill     = regexp.MustCompile(`(?is)^update\s+evidence_agg\s+set\s+evidence_quality\s*=\s*''\s+where\s+result\s*=\s*'PASS'$`)
)

func ValidateMigrationSQL(name, sql string) error {
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("migration %s is empty", name)
	}

	// Automatic production migration is an allowlist, not a blacklist. This
	// intentionally accepts the R2C-152 fixture (ADD COLUMN plus its bounded
	// evidence-quality backfill) and sends every other SQL shape to the manual
	// gate. Splitting first prevents a harmless first statement from hiding a
	// destructive second statement on the same line.
	clean := blockComment.ReplaceAllString(lineComment.ReplaceAllString(sql, ""), "")
	statements := strings.Split(clean, ";")
	seen := 0
	for _, raw := range statements {
		statement := strings.TrimSpace(raw)
		if statement == "" {
			continue
		}
		seen++
		if addColumnStatement.MatchString(statement) || r2c152Backfill.MatchString(statement) {
			continue
		}
		return fmt.Errorf("migration %s contains a statement outside the automatic additive allowlist: %s", name, statement)
	}
	if seen == 0 {
		return fmt.Errorf("migration %s contains no SQL statements", name)
	}
	return nil
}
