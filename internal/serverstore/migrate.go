package serverstore

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migration is one embedded .sql file, split into executable statements.
// Version is the file name; lexicographic order is execution order
// (0001_..., 0002_...).
type Migration struct {
	Version    string
	Statements []string
}

// LoadMigrations reads the embedded migrations directory in order.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("serverstore: read migrations: %w", err)
	}
	var migs []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("serverstore: read %s: %w", e.Name(), err)
		}
		stmts := splitStatements(string(raw))
		if len(stmts) == 0 {
			return nil, fmt.Errorf("serverstore: migration %s has no statements", e.Name())
		}
		migs = append(migs, Migration{Version: e.Name(), Statements: stmts})
	}
	if len(migs) == 0 {
		return nil, fmt.Errorf("serverstore: no migrations embedded")
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].Version < migs[j].Version })
	return migs, nil
}

// splitStatements strips "--" line comments and splits on ';'. Migration
// files must therefore keep semicolons out of string literals — a documented
// constraint of this deliberately tiny runner.
func splitStatements(sqlText string) []string {
	var b strings.Builder
	for _, line := range strings.Split(sqlText, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	var out []string
	for _, chunk := range strings.Split(b.String(), ";") {
		if s := strings.TrimSpace(chunk); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Migrate applies every embedded migration not yet recorded in
// schema_migrations, each inside its own transaction. It is idempotent and
// safe to run on every server start.
func Migrate(ctx context.Context, conn *pgx.Conn) error {
	migs, err := LoadMigrations()
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		return fmt.Errorf("serverstore: create schema_migrations: %w", err)
	}
	for _, m := range migs {
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`,
			m.Version).Scan(&applied); err != nil {
			return fmt.Errorf("serverstore: check migration %s: %w", m.Version, err)
		}
		if applied {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *pgx.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("serverstore: begin migration %s: %w", m.Version, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	for _, stmt := range m.Statements {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("serverstore: migration %s failed on %q: %w", m.Version, firstLine(stmt), err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations(version) VALUES($1)`, m.Version); err != nil {
		return fmt.Errorf("serverstore: record migration %s: %w", m.Version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("serverstore: commit migration %s: %w", m.Version, err)
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
