package scanner

import (
	"context"
	"os"
	"path/filepath"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
)

// PackageChecker upgrades UNKNOWN publicness verdicts in place.
// *registry.Checker satisfies it; the indirection exists because
// internal/registry imports this package (interface breaks the cycle).
type PackageChecker interface {
	CheckAll(ctx context.Context, pkgs []ResolvedPackage)
}

// ScanResult is everything one project scan produced (contract C14 step 1).
type ScanResult struct {
	Adapters []Adapter
	Packages []ResolvedPackage
	Symbols  []SymbolUsage
	// Edges is the dependency tree, when the adapter's lockfile records one.
	// Empty is not "no dependencies": it is "this ecosystem's lockfile does
	// not say", which is the case for go.mod.
	Edges []Edge
	Env   domain.EnvironmentFingerprint

	// TargetContext is a best-effort TARGET execution-context hint detected
	// from project clues (bun.lock/bun.lockb → "bun", deno.json(c) → "deno",
	// an "electron" dependency → "electron"). Per docs/execution-context.md
	// it is used only to widen search environments; it is deliberately NOT
	// part of Env — build/test observations stay in the toolchain context
	// they actually executed in.
	TargetContext string
}

// Scan orchestrates one project scan: per-adapter package scanning
// (lockfile-resolved), a publicness upgrade pass, per-adapter symbol
// extraction, and environment collection from merged adapter hints
// (first adapter wins per key). ads is the detected adapter set —
// callers obtain it via adapters.Detect(dir). A nil checker skips the
// publicness pass, so packages stay UNKNOWN (= excluded from evidence,
// the safe default). Adapter errors are non-fatal: scanning is
// best-effort and must never break the wrapped command.
func Scan(ctx context.Context, dir string, ads []Adapter, checker PackageChecker) (*ScanResult, error) {
	res := &ScanResult{Adapters: ads}

	type span struct{ start, end int }
	spans := make([]span, len(ads))
	hints := map[string]string{}
	for i, a := range ads {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start := len(res.Packages)
		if pkgs, err := a.ScanPackages(ctx, dir); err == nil {
			res.Packages = append(res.Packages, pkgs...)
		}
		spans[i] = span{start, len(res.Packages)}
		// The tree, when this ecosystem's lockfile records one. Best-effort
		// like everything else here: an adapter that errors contributes no
		// edges and never breaks the wrapped command.
		if es, ok := a.(EdgeScanner); ok {
			if edges, err := es.ScanEdges(ctx, dir); err == nil {
				res.Edges = append(res.Edges, edges...)
			}
		}
		for k, v := range a.EnvironmentHints(ctx, dir) {
			if _, seen := hints[k]; !seen {
				hints[k] = v // first adapter wins per key
			}
		}
	}

	if checker != nil {
		checker.CheckAll(ctx, res.Packages)
	}

	for i, a := range ads {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if syms, err := a.ScanSymbols(ctx, dir, res.Packages[spans[i].start:spans[i].end]); err == nil {
			res.Symbols = append(res.Symbols, syms...)
		}
	}

	res.Env = environment.Collect(ctx, hints)
	res.TargetContext = detectTargetContext(dir, res.Packages)
	return res, nil
}

// Classify maps argv to an observation stage by asking each detected
// adapter in order; the first confident classification wins. Commands no
// adapter knows come back with Known=false and are recorded as USED only.
func (r *ScanResult) Classify(argv []string) CommandProfile {
	for _, a := range r.Adapters {
		if p := a.ClassifyCommand(argv); p.Known {
			return p
		}
	}
	return CommandProfile{}
}

// detectTargetContext finds execution-context clues per
// docs/execution-context.md §5: bun/deno marker files and an electron
// dependency. First match wins in that order.
func detectTargetContext(dir string, pkgs []ResolvedPackage) string {
	for _, f := range []string{"bun.lockb", "bun.lock"} {
		if fileExistsAt(filepath.Join(dir, f)) {
			return "bun"
		}
	}
	for _, f := range []string{"deno.json", "deno.jsonc"} {
		if fileExistsAt(filepath.Join(dir, f)) {
			return "deno"
		}
	}
	for _, p := range pkgs {
		if p.PURL.Ecosystem == "npm" && p.PURL.Name == "electron" {
			return "electron"
		}
	}
	return ""
}

func fileExistsAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
