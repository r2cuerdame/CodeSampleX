package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/adapters"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// ResolvedEdges is the dependency tree this sample's resolver actually wrote
// into the workspace: (parent, child) pairs with both ends at concrete
// versions, exactly as the lockfile records them.
//
// These are DIRECT edges, which is what makes them reportable at all. A
// receipt's resolved-package list is a flat closure, and turning that into
// edges would claim transitive dependencies as direct — an assertion nothing
// measured. The lockfile says who pulled whom; that is the fact, and it is the
// half of a version conflict a person can actually act on.
func ResolvedEdges(ctx context.Context, dir string, m domain.SampleManifest, all []scanner.Adapter) []scanner.Edge {
	// One verification runs one resolver. A lockfile for another ecosystem may
	// merely be shipped beside the sample, and reading it would turn an npm
	// PASS into a Cargo claim that nothing ran — the same rule resolvedPackages
	// applies to versions, applied here to edges.
	resolverEcosystem := strings.ToLower(strings.TrimSpace(m.Environment.Ecosystem))
	if resolverEcosystem == "" {
		return nil
	}

	seen := map[[2]string]bool{}
	var out []scanner.Edge
	for _, a := range all {
		es, ok := a.(scanner.EdgeScanner)
		if !ok || !a.Detect(dir) {
			continue
		}
		// Best-effort, like every other read of a lockfile: an adapter that
		// errors contributes no edges and never fails the verification. The
		// contract passing is the answer this job exists for; the tree is a
		// fact taken on the way past.
		edges, err := es.ScanEdges(ctx, dir)
		if err != nil {
			continue
		}
		for _, e := range edges {
			if e.Parent.Ecosystem != resolverEcosystem || e.Child.Ecosystem != resolverEcosystem {
				continue
			}
			// An end without a concrete version is a range that never
			// resolved. Reporting it would name a dependency at a version
			// nobody installed.
			if !domain.ConcreteResolvedVersion(e.Parent.Version) || !domain.ConcreteResolvedVersion(e.Child.Version) {
				continue
			}
			key := [2]string{e.Parent.String(), e.Child.String()}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Parent.String(), out[j].Parent.String(); a != b {
			return a < b
		}
		return out[i].Child.String() < out[j].Child.String()
	})
	return out
}

// TreeBatches is the dependency tree a verification resolved, as the wire
// units that carry edges.
//
// The batches are deliberately identical in identity to the ones the server
// already derives from this receipt — same peer, same sample-derived bucket,
// same day. A verification whose edges landed in a different bucket from its
// own observations would be counted as two projects, and a count of projects
// is the entire meaning of the number those edges feed.
//
// Nothing here is anybody's private code. A cross-verification workspace is a
// fresh temp directory holding a published sample and whatever the public
// registry resolved into it; the machine's own projects are never in it. That
// is why this path needs no publicness pass, and why it must never be pointed
// at a directory a person works in.
func TreeBatches(edges []scanner.Edge, resolved []string, m domain.SampleManifest, r domain.VerificationReceipt, epoch string) []domain.ObservationBatch {
	bucket := domain.SampleProjectBucket(r.SampleID)
	if bucket == "" || r.PeerID == "" || (len(edges) == 0 && len(resolved) == 0) {
		return nil
	}
	env := r.Environment.Normalize()
	// Exactly what the ingest path requires, checked here so a batch this
	// build cannot get accepted is never built.
	//
	// The receipt backfill is why this is a guard and not a hope: it produced
	// 9,883 observations, the store refused every one of them on a field the
	// unit tests never exercised, and the run reported the refusals as a bare
	// count that read like success. A fingerprint with no schema version has
	// a shape we cannot vouch for, and stamping one on would be labelling it
	// rather than reading it.
	if env.SchemaVersion != 1 || env.Ecosystem == "" || env.OS == "" || env.Arch == "" {
		return nil
	}

	// The sample says which packages it chose; everything else in the tree
	// arrived through somebody else's manifest.
	declared := map[string]bool{}
	for _, raw := range m.Packages {
		if p, err := domain.ParsePURL(raw); err == nil {
			declared[p.String()] = true
		}
	}

	children := map[string][]string{}
	var parents []string
	for _, e := range edges {
		p := e.Parent.String()
		if _, seen := children[p]; !seen {
			parents = append(parents, p)
		}
		children[p] = append(children[p], e.Child.String())
	}

	// A declared package the resolver placed, with no edges of its own, is a
	// leaf — and saying nothing about it is not the same as saying that.
	//
	// The dependency axis answers a release only when it appears as a PARENT
	// of an edge, so a leaf could never be answered: 490 coordinates on
	// production appear as a child of some resolved tree and never as a
	// parent, a quarter of everything open on that axis and unreachable by
	// any amount of farm work.
	//
	// leaves is separate from children so the claim is explicit on the wire.
	// It is made only for a package this resolution actually placed, because
	// a package the lockfile never contained was not measured at all.
	leaves := map[string]bool{}
	for _, raw := range resolved {
		if _, hasEdges := children[raw]; hasEdges {
			continue
		}
		if _, alreadyListed := leaves[raw]; !alreadyListed {
			leaves[raw] = true
			parents = append(parents, raw)
		}
	}
	sort.Strings(parents)

	out := make([]domain.ObservationBatch, 0, len(parents))
	for _, p := range parents {
		kids := children[p]
		// One package's own direct dependencies are a handful. Past the wire
		// cap the server refuses the batch, so clamp rather than lose the
		// whole tree over one unusual package.
		if len(kids) > domain.MaxDependsOnPerBatch {
			kids = kids[:domain.MaxDependsOnPerBatch]
		}
		out = append(out, domain.ObservationBatch{
			SchemaVersion:    1,
			Epoch:            epoch,
			AnonID:           r.PeerID,
			ProjectBucket:    bucket,
			Package:          p,
			Environment:      env,
			Stage:            domain.StageUsed,
			Result:           domain.ResultPass,
			ObservationCount: 1,
			Direct:           declared[p],
			DependsOn:        kids,
			DependsOnNone:    leaves[p],
		})
	}
	return out
}

// reportResolvedTree sends the dependency tree this verification resolved.
//
// Every verification resolves a real lockfile in a container, and that file is
// the only place the tree is ever written down. It was deleted with the
// workspace: the network knew a sample for 1,766 of 3,138 public coordinates
// and a dependency tree for 563, because edges arrived from exactly one source
// — people running `csx run` on their own projects. The farm did all this
// resolving and contributed none of it.
//
// The receipt cannot carry the tree. SigningBytes canonicalises the whole
// struct, so a peer on an older build would decode a receipt with a new field,
// drop it, recompute different signing bytes and reject a receipt that is
// perfectly valid — a new field there breaks cross-verification for everyone
// who has not upgraded. The edges therefore leave by the wire path that
// already exists for them, which the server already turns into
// dependency_edge rows.
//
// Best-effort by construction. The job this loop exists for is the receipt;
// a tree that cannot be read or cannot be delivered must never cost a
// verification that already ran, so every failure here is silent.
func (cv *CrossVerifier) reportResolvedTree(ctx context.Context, dir string, m domain.SampleManifest, r domain.VerificationReceipt) {
	// Only when the resolve stage passed. A resolve that failed left no
	// lockfile, or a partial one, and a tree read out of that would name
	// dependencies at versions nothing installed. Whether the CONTRACT passed
	// is deliberately not part of the gate: a sample that builds and then
	// fails its assertion resolved exactly the same dependencies, and that is
	// still true of the package.
	if r.Stages["resolve"] != sandbox.ResultPass {
		return
	}
	edges := ResolvedEdges(ctx, dir, m, adapters.All())
	// Which of the sample's own declared packages this resolution actually
	// placed, at a concrete version. A package the lockfile never contained
	// was not measured, and must not be reported either way.
	resolved := resolvedPackages(dir, m)
	batches := TreeBatches(edges, resolved, m, r, time.Now().UTC().Format("2006-01-02"))
	if len(batches) == 0 {
		return
	}

	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{"batches": batches}); err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cv.base()+"/v1/evidence/batches", bytes.NewReader(body.Bytes()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cv.client().Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = resp.Body.Read(make([]byte, 0))
}
