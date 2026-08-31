package goadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// ScanEdges reports the dependency tree the Go tool actually built.
//
// Go was the largest ecosystem on this farm and contributed zero dependency
// edges: 8,620 edges in production on 2026-08-31, 8,267 npm and 353 pypi, and
// not one golang, because this adapter had no EdgeScanner and ResolvedEdges
// therefore found nothing that could read a tree here.
//
// Go has no single lockfile that records the tree, which is why it was last.
// The two halves live apart, and the resolve stage already writes both:
//
//	go mod download                        -> .csx-vendor/gomod (each module's own go.mod)
//	go list -m -json all                   -> .csx-vendor/go-modules.json (what was SELECTED)
//
// A module's own go.mod says WHO it depends on. The build list says WHICH
// VERSION of that dependency was compiled and tested. Both are needed, and
// taking versions from the wrong one is the mistake this ecosystem invites:
// Minimal Version Selection resolves a requirement to the maximum requested
// anywhere in the graph, so a module requiring v1.2.0 is routinely built
// against v1.5.0. Reporting v1.2.0 would name a real version of a real module
// that nobody installed — a false statement that looks exactly like a true
// one.
//
// So edge existence comes from the requiring module's go.mod, and both
// endpoints' versions come from the build list. An edge is emitted only when
// both ends appear there, which also drops requirements that belong to another
// platform's build tags or to a dependency's own tests: never selected, never
// downloaded, no version to honestly name.
//
// A require marked `// indirect` is skipped, and that exclusion is most of
// what makes these edges true. Since module graph pruning, a go.mod lists its
// whole transitive closure so the build is reproducible — bbolt's names
// go-spew, which bbolt does not import; testify does. Emitting those produced
// `go.etcd.io/bbolt@v1.4.3 -> github.com/davecgh/go-spew@v1.1.1` on the first
// real workspace this was run against, indistinguishable from the correct
// edges beside it. Deriving edges from a flat closure is the very thing
// ResolvedEdges refuses to do; go.mod simply carries its own closure inline,
// one layer further down.
//
// Publicness is not decided here. Every edge is returned and the caller drops
// the ones whose ends are not public, the same way it does for packages.
func (a *Adapter) ScanEdges(_ context.Context, dir string) ([]scanner.Edge, error) {
	selected, err := goBuildList(dir)
	if err != nil {
		return nil, err
	}
	var out []scanner.Edge
	for parent, parentVersion := range selected {
		f, err := parseCachedGoMod(dir, parent, parentVersion)
		if err != nil {
			// Best effort, like every other read of a lockfile: a module whose
			// go.mod cannot be read contributes no edges and never fails the
			// verification.
			continue
		}
		for _, r := range f.Require {
			if r == nil || r.Indirect {
				continue
			}
			childVersion, ok := selected[r.Mod.Path]
			if !ok {
				continue
			}
			if r.Mod.Path == parent {
				continue
			}
			out = append(out, scanner.Edge{
				Parent: domain.PURL{Ecosystem: goEcosystem, Name: parent, Version: parentVersion},
				Child:  domain.PURL{Ecosystem: goEcosystem, Name: r.Mod.Path, Version: childVersion},
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Parent.String(), out[j].Parent.String(); a != b {
			return a < b
		}
		return out[i].Child.String() < out[j].Child.String()
	})
	return out, nil
}

const goEcosystem = "golang"

// goBuildList is what `go list -m -json all` selected, keyed by module path.
//
// The main module is excluded: it is the sample's throwaway wrapper, not a
// coordinate anything can depend on.
//
// A replace directive is resolved here rather than at the edge, because it
// decides identity for both ends at once. A replacement that keeps the path
// and only moves the version is reported at the version that ran. One that
// changes the path — a fork, or a local directory — cannot be named by the
// declared purl at all, so the module is dropped from the build list entirely
// and neither end of any edge can reach it. That is the rule goListResolved
// already applies to resolved package versions; an edge disagreeing with it
// would leave the network holding two conflicting answers about one module.
func goBuildList(dir string) (map[string]string, error) {
	body, err := os.ReadFile(filepath.Join(dir, ".csx-vendor", "go-modules.json"))
	if err != nil {
		return nil, err
	}
	selected := map[string]string{}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	for {
		var mod struct {
			Path    string `json:"Path"`
			Version string `json:"Version"`
			Main    bool   `json:"Main"`
			Replace *struct {
				Path    string `json:"Path"`
				Version string `json:"Version"`
			} `json:"Replace"`
		}
		if err := dec.Decode(&mod); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if mod.Main || mod.Path == "" {
			continue
		}
		version := mod.Version
		if mod.Replace != nil {
			if mod.Replace.Path != mod.Path || mod.Replace.Version == "" {
				continue
			}
			version = mod.Replace.Version
		}
		if version == "" {
			continue
		}
		selected[mod.Path] = version
	}
	if len(selected) == 0 {
		return nil, errors.New("goadapter: .csx-vendor/go-modules.json names no selected module")
	}
	return selected, nil
}

// parseCachedGoMod reads one module's own go.mod out of the download cache.
//
// The path is case-folded on the way in: the cache stores an upper-case letter
// as a bang followed by its lower-case form, so github.com/Microsoft/go-winio
// is written to github.com/!microsoft/go-winio. Joining the raw path finds
// nothing on a case-sensitive filesystem, and the module silently contributes
// no edges rather than failing anything — which is how it would have been
// missed.
//
// ParseLax rather than Parse: this is a dependency's go.mod, not the main
// module's. It may use directives from a newer Go than the one reading it, and
// the go command itself reads other modules leniently for the same reason.
// Only the require list is wanted here.
func parseCachedGoMod(dir, path, version string) (*modfile.File, error) {
	escapedPath, err := module.EscapePath(path)
	if err != nil {
		return nil, err
	}
	escapedVersion, err := module.EscapeVersion(version)
	if err != nil {
		return nil, err
	}
	at := filepath.Join(dir, ".csx-vendor", "gomod", "cache", "download",
		filepath.FromSlash(escapedPath), "@v", escapedVersion+".mod")
	body, err := os.ReadFile(at)
	if err != nil {
		return nil, err
	}
	return modfile.ParseLax(at, body, nil)
}
