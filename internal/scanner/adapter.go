// Package scanner defines the ecosystem adapter contract and orchestrates
// project scanning. Adapters keep language quirks out of the core
// (goal.md §20.4); each publishes only the capabilities it truly has (§13.1).
package scanner

import (
	"context"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Publicness of a resolved package. UNKNOWN is treated as private for
// evidence purposes — the safe default (goal.md §8.1).
const (
	PublicnessPublic  = "PUBLIC"
	PublicnessPrivate = "PRIVATE"
	PublicnessUnknown = "UNKNOWN"
)

// ResolvedPackage is one dependency with its lockfile-resolved version
// (goal.md §7.1 — never a manifest range when a lockfile exists).
type ResolvedPackage struct {
	PURL       domain.PURL
	Publicness string // PUBLIC | PRIVATE | UNKNOWN; PRIVATE/UNKNOWN never leave the machine
	Direct     bool   // direct dependency vs transitive
	Source     string // which file resolved it, local diagnostic only (never uploaded)
}

// SymbolUsage is one observed public-symbol use in the local project.
type SymbolUsage struct {
	Package    domain.PURL
	Family     string // e.g. "axios.post", "serde::Deserialize", "github.com/x/y.Func"
	Kind       string // function | class | method | property | module
	Confidence domain.SymbolConfidence
}

// CommandProfile classifies a command the user ran through csx.
type CommandProfile struct {
	Stage domain.Stage // PROJECT_TYPECHECK | PROJECT_COMPILE | PROJECT_TEST | PROJECT_PROCESS
	Known bool         // false ⇒ unclassified, recorded as PROJECT_PROCESS only if Known
	Tool  string       // "tsc", "go", "cargo", ... diagnostic only
}

// Adapter is implemented once per ecosystem (adapters/node, adapters/python,
// adapters/goadapter, adapters/rust).
type Adapter interface {
	// Ecosystem returns the purl ecosystem: "npm" | "pypi" | "golang" | "cargo".
	Ecosystem() string
	// Capabilities returns honest capability levels, e.g. ["A0","A1","A2"].
	Capabilities() []string
	// Detect reports whether dir looks like a project of this ecosystem.
	Detect(dir string) bool
	// ScanPackages returns dependencies with lockfile-resolved versions.
	// Publicness is set to PRIVATE for file:/link:/git/workspace/path deps;
	// otherwise UNKNOWN (a separate registry check upgrades to PUBLIC).
	ScanPackages(ctx context.Context, dir string) ([]ResolvedPackage, error)
	// ScanSymbols extracts public-symbol usages for the given packages.
	ScanSymbols(ctx context.Context, dir string, pkgs []ResolvedPackage) ([]SymbolUsage, error)
	// ClassifyCommand maps an argv to an observation stage.
	ClassifyCommand(argv []string) CommandProfile
	// EnvironmentHints returns fingerprint hints (JSON field names of
	// domain.EnvironmentFingerprint) for environment.Collect.
	EnvironmentHints(ctx context.Context, dir string) map[string]string
}
