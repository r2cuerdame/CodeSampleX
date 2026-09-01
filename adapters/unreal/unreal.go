// Package unreal recognises an Unreal Engine project and reports which engine
// it is built against.
//
// It scans no packages, and that is the point rather than a gap. Unreal's
// dependencies are engine modules and marketplace plugins; neither has a
// stable public identifier this network could hold anyone to, so claiming any
// would be inventing coordinates. What an Unreal project DOES have that is
// public and stable is the engine version it targets, and the vocabulary for
// it already existed -- engine/unreal -- with nothing producing it.
//
// Measured 2026-09-01 before this adapter existed: evidence.Scan on a minimal
// .uproject returned packages=0, edges=0, and an environment with no
// ecosystem, runtime, language or frameworks. Wrapping a UBT build in
// run_observed_command therefore recorded nothing at all -- the exit code
// passed through and the network learned no more than if it had never run.
package unreal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// Adapter reports the engine an Unreal project targets.
type Adapter struct{}

// New returns the Unreal adapter.
func New() Adapter { return Adapter{} }

var _ scanner.Adapter = Adapter{}

// Ecosystem is "generic": Unreal is not a package registry, and the only
// coordinate this adapter can name lives in the fixed public target
// vocabulary rather than in any ecosystem's namespace.
func (Adapter) Ecosystem() string { return "generic" }

// Capabilities is deliberately the narrowest one. This adapter detects a
// project and reads one field; it resolves nothing and executes nothing.
func (Adapter) Capabilities() []string { return []string{"A0"} }

// Detect reports whether dir holds a .uproject file.
func (Adapter) Detect(dir string) bool { return projectFile(dir) != "" }

// ScanPackages returns the engine the project targets, and nothing else.
//
// It has to return something, or an Unreal project records nothing: an
// observation is written per PUBLIC package a scan found, so a project with
// no packages produces no rows at all however the build went.
//
// The engine is the one coordinate here that is public and stable, and
// domain.IsWantedTarget is already documented as the publicness boundary for
// targets that do not live on a package registry. So it is marked PUBLIC
// outright rather than sent to a registry that has never heard of it.
//
// Marketplace plugins and engine modules are still not reported. That is a
// different judgement rather than this one relaxed: they have no stable
// public identifier, so any coordinate for them would be invented.
func (a Adapter) ScanPackages(ctx context.Context, dir string) ([]scanner.ResolvedPackage, error) {
	p, ok := a.engine(ctx, dir)
	if !ok {
		return nil, nil
	}
	return []scanner.ResolvedPackage{{
		PURL:       p,
		Publicness: scanner.PublicnessPublic,
		Direct:     true,
		Source:     "uproject",
	}}, nil
}

// engine resolves the .uproject to its public engine coordinate.
func (Adapter) engine(_ context.Context, dir string) (domain.PURL, bool) {
	descriptor := engineDescriptor(dir)
	if descriptor == "" {
		return domain.PURL{}, false
	}
	return domain.WantedTargetFromFramework(descriptor)
}

// ScanSymbols returns nothing: there are no packages to attribute symbols to.
func (Adapter) ScanSymbols(context.Context, string, []scanner.ResolvedPackage) ([]scanner.SymbolUsage, error) {
	return nil, nil
}

// ClassifyCommand claims no command. Unreal builds run through UnrealBuildTool
// and Build.bat under names this adapter cannot recognise without guessing,
// and a guess here would label somebody else's command with this ecosystem.
func (Adapter) ClassifyCommand([]string) scanner.CommandProfile {
	return scanner.CommandProfile{}
}

// EnvironmentHints reports the engine as a framework descriptor.
//
// "unreal@5.5" and not "unreal", because the bare name converts to nothing:
// WantedTargetFromFramework requires "<name>@<version>" with a concrete
// version. An EngineAssociation that is a GUID -- what a source build writes
// there -- yields no hint at all, since an unconvertible descriptor would
// travel as an arbitrary framework string and mean nothing to anyone.
func (Adapter) EnvironmentHints(_ context.Context, dir string) map[string]string {
	descriptor := engineDescriptor(dir)
	if descriptor == "" {
		return nil
	}
	return map[string]string{"frameworks": descriptor}
}

// engineDescriptor reads the .uproject and returns "unreal@<version>", or ""
// when nothing convertible is there.
func engineDescriptor(dir string) string {
	path := projectFile(dir)
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		EngineAssociation string `json:"EngineAssociation"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	descriptor := "unreal@" + strings.TrimSpace(doc.EngineAssociation)
	if _, ok := domain.WantedTargetFromFramework(descriptor); !ok {
		return ""
	}
	return descriptor
}

// projectFile returns the .uproject in dir, or "" when there is none. Only
// the top level: a .uproject nested in a plugin or a sample belongs to that
// thing, not to the directory the developer is working in.
func projectFile(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".uproject") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}
