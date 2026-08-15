package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/adapters"
	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func init() {
	Register(Command{
		Name:    "scan",
		Summary: "record which public packages a project (or a tree of projects) uses, without building",
		Run:     scanMain,
	})
}

// maxScanDepth bounds the walk. Repositories nest a few levels; going
// deeper mostly finds vendored copies and build output.
const maxScanDepth = 4

// parseInterspersed parses flags that appear before, between or after
// positional arguments and returns the positionals in order. The stdlib
// parser alone stops at the first positional, which silently drops any
// flag typed after a path.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		rest = fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
}

// scanMain implements `csx scan [dir] [--recursive] [--dry-run]`.
//
// It records USED evidence only: which public packages, at which
// lockfile-resolved versions, appear in a project. That is deliberately
// the weakest evidence class (goal.md §6.1.A) — nothing was built, so
// nothing may claim a build passed. It exists because the cold-start
// problem is real: a developer with twenty repositories on disk can
// contribute the shape of twenty real dependency trees in one command,
// where `csx run` would require twenty builds.
func scanMain(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	recursive := fs.Bool("recursive", false, "scan every project found under the directory")
	dryRun := fs.Bool("dry-run", false, "show what would be recorded, record nothing")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: csx scan [dir] [--recursive] [--dry-run]")
		fmt.Fprintln(os.Stderr, "\nRecords USED evidence for the public packages a project depends on.")
		fmt.Fprintln(os.Stderr, "Nothing is built, so nothing is claimed beyond \"this package is in use\".")
		fs.PrintDefaults()
	}
	// Go's flag package stops parsing at the first positional argument, so
	// "csx scan ./dir --dry-run" would silently ignore --dry-run and record
	// evidence the user explicitly asked not to record. Parse in a loop so
	// flags are honored wherever they appear.
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	root := "."
	if len(positional) > 0 {
		root = positional[0]
	}
	abs, absErr := filepath.Abs(root)
	if absErr != nil {
		fmt.Fprintf(os.Stderr, "csx scan: %v\n", absErr)
		return 1
	}

	env, err := openScanEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx scan: %v\n", err)
		return 1
	}
	defer env.close()

	dirs := []string{abs}
	if *recursive {
		dirs = findProjects(abs)
		if len(dirs) == 0 {
			fmt.Fprintf(os.Stderr, "csx scan: no projects found under %s\n", abs)
			return 1
		}
	}

	var scanned, publicTotal int
	for _, dir := range dirs {
		res, err := evidence.Scan(ctx, dir, env.checker)
		if err != nil || res == nil || len(res.Packages) == 0 {
			continue
		}
		scanned++
		counts := publicnessCounts(res)
		publicTotal += counts["PUBLIC"]
		fmt.Printf("%s\n  %d public · %d private · %d unknown\n",
			shortDir(abs, dir), counts["PUBLIC"], counts["PRIVATE"], counts["UNKNOWN"])

		if *dryRun {
			continue
		}
		// USED/PASS only: an unclassified command profile is exactly the
		// "these packages are in use, nothing more" record (C14).
		if err := env.rec.RecordRun(ctx, dir, res, scanner.CommandProfile{}, 0, ""); err != nil {
			fmt.Fprintf(os.Stderr, "  record failed: %v\n", err)
		}
	}

	if scanned == 0 {
		fmt.Fprintln(os.Stderr, "csx scan: nothing to scan (no manifest or lockfile found)")
		return 1
	}
	fmt.Printf("\n%d project(s), %d public package sightings\n", scanned, publicTotal)
	if *dryRun {
		fmt.Println("dry run: nothing recorded")
		return 0
	}
	if env.cfg.Mode == config.ModeCommunity {
		fmt.Println("run `csx sync` to upload, or let the daemon do it on its next tick")
	}
	return 0
}

// scanEnv holds the local stores a scan needs. Publicness checks only run
// once a mode has been chosen, so an uninitialized home records nothing
// uploadable — the safe default.
type scanEnv struct {
	db      *localdb.DB
	cfg     *config.Config
	rec     *evidence.Recorder
	checker *registry.Checker
}

func openScanEnv() (*scanEnv, error) {
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	if err := config.EnsureHome(home); err != nil {
		return nil, err
	}
	cfg, err := config.Load(home)
	if err != nil {
		return nil, err
	}
	ident, err := identity.LoadOrCreate(home)
	if err != nil {
		return nil, err
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		return nil, err
	}
	e := &scanEnv{db: db, cfg: cfg, rec: &evidence.Recorder{DB: db, Ident: ident, Cfg: cfg}}
	// Community mode only: see config.MayContactRegistries.
	if config.MayContactRegistries(cfg.Mode) {
		e.checker = &registry.Checker{Cache: evidence.PublicnessCache{DB: db}}
	}
	return e, nil
}

func (e *scanEnv) close() {
	if e.db != nil {
		e.db.Close()
	}
}

func publicnessCounts(res *scanner.ScanResult) map[string]int {
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, p := range res.Packages {
		key := p.PURL.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		counts[p.Publicness]++
	}
	return counts
}

// skipDirs never contain a project worth scanning on its own: they hold
// dependency copies and build output, whose versions are already recorded
// through the lockfile of the project that owns them.
var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "vendor": true, "dist": true,
	"build": true, "target": true, "venv": true, ".venv": true,
	"__pycache__": true, ".next": true, "out": true, "coverage": true,
}

// findProjects walks root and returns every directory that looks like a
// project root. A matched directory is not descended into: its
// subdirectories belong to it, not to separate projects.
func findProjects(root string) []string {
	var found []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // unreadable dirs are skipped, not fatal
		}
		name := d.Name()
		if p != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
			return filepath.SkipDir
		}
		if depth(root, p) > maxScanDepth {
			return filepath.SkipDir
		}
		if isProjectDir(p) {
			found = append(found, p)
			return filepath.SkipDir
		}
		return nil
	})
	sort.Strings(found)
	return found
}

func depth(root, p string) int {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(rel, string(filepath.Separator)))
}

// isProjectDir reports whether any registered adapter recognizes the dir.
func isProjectDir(dir string) bool {
	return len(adapters.Detect(dir)) > 0
}

// shortDir renders a scanned path relative to the scan root so output
// never echoes a long absolute path back at the user.
func shortDir(root, dir string) string {
	if rel, err := filepath.Rel(root, dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return filepath.Base(dir)
}
