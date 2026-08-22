package httpapi

import (
	"context"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// maxDependencyProbesPerRequest bounds how many NEW dependency coordinates one
// poll of /v1/authoring/work/next may check against a public registry.
//
// Dependency work is the one queue source whose coordinates the network has
// never held a package row for: they exist because somebody's lockfile
// resolved onto them, and nothing has confirmed them since. Confirming a
// window's worth would be up to two hundred sequential probes at npmjs.org
// per poll, from a fleet that polls several times a minute — which is how a
// host gets blocked, and it would hold the handler open for minutes while it
// happened.
//
// Four is enough. A worker takes ONE job per poll, and the candidate order is
// stable, so successive polls walk further down the list while everything
// already confirmed costs nothing: a confirmed coordinate leaves a PUBLIC
// package row behind and is free from then on. The unconfirmed remainder is
// dropped from this pass rather than refused, which is the difference between
// "not yet asked" and "answered no".
const maxDependencyProbesPerRequest = 4

// confirmDependencyWork keeps the DEPENDENCY candidates a public registry
// confirms and drops the rest of them from this pass.
//
// It exists because a dependency coordinate is the only work this server
// hands out that no publicness gate has already seen. The ingest gate checks
// every dependsOn child, but only outside trust mode and only within its own
// per-request lookup cap, so an edge can reach the table unconfirmed. Absolute
// principle 2 limits automatic collection to packages that exist on a public
// registry, and a coordinate this server tells a worker to build is squarely
// inside that boundary.
//
// The confirmation is not only a filter. registry.Checker writes its verdict
// through to the packages table, so confirming a coordinate is also what
// registers it: the release stops being an edge nobody has a row for and
// becomes a package the registry endpoints, the version axis and the next
// scheduling pass can all see.
func (a *api) confirmDependencyWork(ctx context.Context, candidates []serverstore.WantedRow) []serverstore.WantedRow {
	hasDependency := false
	for _, c := range candidates {
		if c.Kind == "DEPENDENCY" {
			hasDependency = true
			break
		}
	}
	if !hasDependency {
		return candidates
	}
	// Trust mode turns the publicness gate off for ingest too; it is dev and
	// e2e only, and a server running it has no checker to ask.
	if a.trustMode() {
		return candidates
	}
	out := make([]serverstore.WantedRow, 0, len(candidates))
	probes := 0
	for _, c := range candidates {
		if c.Kind != "DEPENDENCY" {
			out = append(out, c)
			continue
		}
		// No checker and not trust mode: the server cannot tell whether this
		// coordinate is public, and UNKNOWN is private — the safe default.
		if a.d.Checker == nil {
			continue
		}
		p := domain.PURL{Ecosystem: c.Ecosystem, Name: c.Name, Version: c.Version}
		if pkg, ok, err := a.d.Store.GetPackage(ctx, p.String()); err == nil && ok &&
			pkg.Publicness == scanner.PublicnessPublic {
			// Already a registered public release. Asking again would spend a
			// registry round trip to learn what this server wrote down itself.
			out = append(out, c)
			continue
		}
		if probes >= maxDependencyProbesPerRequest {
			continue
		}
		probes++
		if a.d.Checker.Check(ctx, p) != scanner.PublicnessPublic {
			continue
		}
		out = append(out, c)
	}
	return out
}
