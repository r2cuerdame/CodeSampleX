package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func init() {
	Register(Command{
		Name:    "run",
		Summary: "run a command, recording anonymous usage evidence for public packages",
		Run:     runMain,
	})
}

// runMain implements `csx run -- <command...>` (contract C14). Evidence
// plumbing is strictly best-effort: the wrapped command always runs and
// its exit code always passes through, even when local recording or the
// opportunistic upload is unavailable (goal.md §3.9).
func runMain(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: csx run -- <command...>")
		return 2
	}
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}

	var (
		db    *localdb.DB
		cfg   *config.Config
		ident *identity.Identity
	)
	if home, err := config.Home(); err == nil {
		if err := config.EnsureHome(home); err == nil {
			cfg, _ = config.Load(home)
			ident, _ = identity.LoadOrCreate(home)
			db, _ = localdb.Open(filepath.Join(home, "csx.db"))
		}
	}
	if db != nil {
		defer db.Close()
	}
	if cfg == nil {
		cfg = config.Default()
	}

	// Publicness checks talk to public registries, so they run only after
	// the user chose a mode in csx init; before that everything stays
	// UNKNOWN (= excluded from evidence, the safe default).
	var checker *registry.Checker
	if db != nil && cfg.Mode != config.ModeUninitialized {
		checker = &registry.Checker{Cache: evidence.PublicnessCache{DB: db}}
	}

	res, _ := evidence.Scan(ctx, dir, checker)
	var profile scanner.CommandProfile
	if res != nil {
		profile = res.Classify(args)
	}

	exitCode, tail, runErr := evidence.Run(ctx, args, dir)
	if runErr != nil {
		// The command never ran (not found, ...): report and record nothing.
		fmt.Fprintf(os.Stderr, "csx: %v\n", runErr)
		return 127
	}

	if db != nil && ident != nil && res != nil {
		rec := &evidence.Recorder{DB: db, Ident: ident, Cfg: cfg}
		if err := rec.RecordRun(ctx, dir, res, profile, exitCode, tail); err != nil {
			fmt.Fprintf(os.Stderr, "csx: record evidence: %v\n", err)
		}
		// Opportunistic best-effort upload; failures leave rows queued for
		// the next run or `csx sync` (§25.F).
		batcher := &evidence.Batcher{DB: db, Ident: ident, Cfg: cfg}
		uctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, _ = batcher.Upload(uctx, &http.Client{Timeout: 10 * time.Second}, cfg.ServerURL)
		cancel()
	}
	return exitCode
}
