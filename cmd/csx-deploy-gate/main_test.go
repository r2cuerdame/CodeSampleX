package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const migrationDir = "internal/serverstore/migrations"

// gitRepo builds a throwaway repository with one committed migration so each
// case can layer a second commit on top and diff the two SHAs through the
// same git invocation production uses. autocrlf/safecrlf are pinned off so
// the bytes written here are the bytes git stores, regardless of host config.
type gitRepo struct {
	t   *testing.T
	dir string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	r := &gitRepo{t: t, dir: t.TempDir()}
	r.run("init", "-q")
	r.run("config", "user.email", "gate@test")
	r.run("config", "user.name", "gate")
	r.run("config", "core.autocrlf", "false")
	r.run("config", "core.safecrlf", "false")
	r.run("config", "commit.gpgsign", "false")
	return r
}

func (r *gitRepo) run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *gitRepo) write(name string, content []byte) {
	r.t.Helper()
	path := filepath.Join(r.dir, filepath.FromSlash(migrationDir), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *gitRepo) remove(name string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.dir, filepath.FromSlash(migrationDir), name)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *gitRepo) commit(msg string) string {
	r.t.Helper()
	r.run("add", "-A")
	r.run("commit", "-q", "--allow-empty", "-m", msg)
	return r.run("rev-parse", "HEAD")
}

const (
	migration0032 = "0032_dependency_edge_parent_idx.sql"
	sqlLF         = "CREATE INDEX IF NOT EXISTS dependency_edges_parent_idx\n    ON dependency_edges (parent_package_id);\n"
)

func crlf(s string) []byte {
	return []byte(strings.ReplaceAll(s, "\n", "\r\n"))
}

func TestChangedMigrationsIgnoresLineEndingOnlyNormalization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before []byte
		after  []byte
	}{
		{"crlf to lf", crlf(sqlLF), []byte(sqlLF)},
		{"lf to crlf", []byte(sqlLF), crlf(sqlLF)},
		// The live #347 shape: only the final line carried a CR.
		{"mixed to lf", []byte(strings.TrimSuffix(sqlLF, "\n") + "\r\n"), []byte(sqlLF)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newGitRepo(t)
			r.write(migration0032, tc.before)
			previous := r.commit("base")
			r.write(migration0032, tc.after)
			target := r.commit("normalize line endings")
			if previous == target {
				t.Fatal("fixture did not produce a distinct commit")
			}
			got, err := changedMigrations(r.dir, previous, target)
			if err != nil {
				t.Fatalf("line-ending-only normalization rejected: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("line-ending-only normalization reported migrations %v", got)
			}
		})
	}
}

func TestChangedMigrationsFailsClosedOnSemanticEditOrRemoval(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before []byte
		mutate func(r *gitRepo)
	}{
		{"sql edit", []byte(sqlLF), func(r *gitRepo) {
			r.write(migration0032, []byte(strings.Replace(sqlLF, "parent_package_id", "child_package_id", 1)))
		}},
		{"sql edit hidden inside crlf normalization", crlf(sqlLF), func(r *gitRepo) {
			r.write(migration0032, []byte(strings.Replace(sqlLF, "IF NOT EXISTS ", "", 1)))
		}},
		{"appended statement with crlf normalization", crlf(sqlLF), func(r *gitRepo) {
			r.write(migration0032, []byte(sqlLF+"DROP TABLE dependency_edges;\n"))
		}},
		{"trailing space edit", []byte(sqlLF), func(r *gitRepo) {
			r.write(migration0032, []byte(strings.Replace(sqlLF, ";\n", "; \n", 1)))
		}},
		{"removed", []byte(sqlLF), func(r *gitRepo) {
			r.remove(migration0032)
		}},
		{"renamed", []byte(sqlLF), func(r *gitRepo) {
			r.remove(migration0032)
			r.write("0033_dependency_edge_parent_idx.sql", []byte(sqlLF))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newGitRepo(t)
			r.write(migration0032, tc.before)
			previous := r.commit("base")
			tc.mutate(r)
			target := r.commit("mutate")
			got, err := changedMigrations(r.dir, previous, target)
			if err == nil {
				t.Fatalf("semantic migration change accepted, migrations=%v", got)
			}
			if !strings.Contains(err.Error(), "existing migration changed or was removed") {
				t.Fatalf("unexpected rejection reason: %v", err)
			}
		})
	}
}

func TestChangedMigrationsStillReportsAddedMigrations(t *testing.T) {
	r := newGitRepo(t)
	r.write(migration0032, crlf(sqlLF))
	previous := r.commit("base")
	r.write(migration0032, []byte(sqlLF))
	r.write("0038_new_index.sql", []byte("CREATE INDEX IF NOT EXISTS x ON y (z);\n"))
	target := r.commit("normalize and add")
	got, err := changedMigrations(r.dir, previous, target)
	if err != nil {
		t.Fatalf("additive migration rejected: %v", err)
	}
	if len(got) != 1 || got[0] != "0038_new_index.sql" {
		t.Fatalf("expected only the added migration, got %v", got)
	}
}

func TestChangedMigrationsRejectsNonCanonicalAddedName(t *testing.T) {
	r := newGitRepo(t)
	r.write(migration0032, []byte(sqlLF))
	previous := r.commit("base")
	r.write("hotfix.sql", []byte("CREATE INDEX IF NOT EXISTS x ON y (z);\n"))
	target := r.commit("add non-canonical")
	if _, err := changedMigrations(r.dir, previous, target); err == nil || !strings.Contains(err.Error(), "non-canonical") {
		t.Fatalf("non-canonical migration name accepted: %v", err)
	}
}

func TestChangedMigrationsProductionPair(t *testing.T) {
	const (
		prevProduction = "3a34f6d258a4f760c66f024b611ddca917839e19"
		blockedTarget  = "4b08ed5093740268f50682011e918dc8d3744f35"
	)
	repoRoot := filepath.Join("..", "..")
	if err := exec.Command("git", "-C", repoRoot, "cat-file", "-e", prevProduction).Run(); err != nil {
		t.Skip("repo does not contain previous production commit")
	}
	if err := exec.Command("git", "-C", repoRoot, "cat-file", "-e", blockedTarget).Run(); err != nil {
		t.Skip("repo does not contain target commit")
	}
	got, err := changedMigrations(repoRoot, prevProduction, blockedTarget)
	if err != nil {
		t.Fatalf("production pair 3a34f6d2..4b08ed50 rejected: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 migrations for line-ending normalization pair, got %v", got)
	}
}
