package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/deploygate"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func main() {
	repo := flag.String("repo", ".", "repository root")
	target := flag.String("target", "", "immutable target commit")
	previous := flag.String("previous", "", "previous production commit")
	mergeVerdict := flag.String("merge-verdict", "", "ProjectOps MergeVerdict")
	requiresHuman := flag.String("requires-human-decision", "", "ProjectOps requires_human_decision")
	sideEffectClass := flag.String("side-effect-class", "", "safe or additive-migration")
	flag.Parse()

	if !shaPattern.MatchString(*target) || !shaPattern.MatchString(*previous) {
		fatalf("target and previous must be lowercase immutable 40-character SHAs")
	}
	if err := deploygate.ValidateDecision(deploygate.Request{
		MergeVerdict:          *mergeVerdict,
		RequiresHumanDecision: *requiresHuman,
		SideEffectClass:       *sideEffectClass,
	}); err != nil {
		fatalf("ineligible ProjectOps decision: %v", err)
	}

	requireCommit(*repo, *target)
	requireCommit(*repo, *previous)
	if !ancestor(*repo, *target, "origin/main") {
		fatalf("target %s is not in canonical origin/main", *target)
	}
	if !ancestor(*repo, *previous, *target) {
		fatalf("previous production %s is not an ancestor of target %s", *previous, *target)
	}

	migrations := changedMigrations(*repo, *previous, *target)
	if err := deploygate.ValidateMigrationClass(*sideEffectClass, migrations); err != nil {
		fatalf("migration classification mismatch: %v", err)
	}
	for _, name := range migrations {
		raw := git(*repo, "show", *target+":"+filepath.ToSlash(filepath.Join("internal", "serverstore", "migrations", name)))
		if err := deploygate.ValidateMigrationSQL(name, raw); err != nil {
			fatalf("unsafe migration: %v", err)
		}
	}

	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"eligible":        true,
		"targetSha":       *target,
		"previousSha":     *previous,
		"sideEffectClass": *sideEffectClass,
		"migrations":      migrations,
	})
}

func requireCommit(repo, sha string) {
	if got := strings.TrimSpace(git(repo, "rev-parse", sha+"^{commit}")); got != sha {
		fatalf("%s resolved to %s", sha, got)
	}
}

func ancestor(repo, older, newer string) bool {
	cmd := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", older, newer)
	return cmd.Run() == nil
}

func changedMigrations(repo, previous, target string) []string {
	out := git(repo, "diff", "--name-status", previous+".."+target, "--", "internal/serverstore/migrations")
	var migrations []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "A" {
			fatalf("existing migration changed or was removed: %s", line)
		}
		name := filepath.Base(filepath.FromSlash(fields[1]))
		if !regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+\.sql$`).MatchString(name) {
			fatalf("migration has a non-canonical name: %s", name)
		}
		migrations = append(migrations, name)
	}
	return migrations
}

func git(repo string, args ...string) string {
	argv := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", argv...).CombinedOutput()
	if err != nil {
		fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "production deploy gate: "+format+"\n", args...)
	os.Exit(1)
}
