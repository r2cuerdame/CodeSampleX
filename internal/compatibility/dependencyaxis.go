package compatibility

import (
	"context"
	"log"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// createDependencyAxisJobs opens verification work for coordinates whose
// dependency axis is open.
//
// This is the half of #87 that could not be a sample. A release with a
// passing sample and no dependency answer is invisible to the authoring
// queue -- `fresh` drops everything already in verified_packages -- so on
// production 1,286 of 3,138 coordinates sat on the SE- cell with nothing
// able to move them, while the fleet reported healthy and the panel counted
// the gap every minute.
//
// What closes it is a resolver, not an author. Every verification resolves a
// real lockfile in a container and CrossVerifier reports the tree it wrote;
// the server already turns that into dependency_edge and
// dependency_resolution rows. So the job is an ordinary cross verification of
// the sample the coordinate already has, and the deliverable is the tree
// taken on the way past.
//
// The builder is the clock rather than boot. A boot-only reconcile converges
// once per deploy, and deploys here are batched deliberately (#174) -- the
// queue would then be as stale as the release cadence, which is the shape of
// starvation this issue is about.
//
// Convergence is the store's: DependencyAxisOpen excludes a sample with a
// live job and one that has spent DependencyAxisMaxAttempts, and a coordinate
// whose tree arrives stops being selected at all. So the set shrinks by
// answers and never re-fills with the same question.
func (b *Builder) createDependencyAxisJobs(ctx context.Context) error {
	store, ok := b.Store.(serverstore.DependencyAxisStore)
	if !ok {
		return nil
	}
	work, err := store.DependencyAxisOpen(ctx, serverstore.DependencyAxisMaxAttempts,
		serverstore.DependencyAxisPerPass)
	if err != nil {
		return err
	}
	opened := 0
	for _, w := range work {
		// EnsureCrossJob, not CreateJob: it reuses a live job for the sample
		// under an advisory lock, so two builder passes overlapping during a
		// deploy cannot open the same question twice.
		//
		// WantEnvJSON is empty on purpose. The question is what this
		// ecosystem's resolver writes down, and every lane that can build the
		// sample resolves the same tree, so pinning an environment would
		// narrow the pool of verifiers for no gain in the answer.
		if _, err := b.Store.EnsureCrossJob(ctx, serverstore.JobRow{
			SampleID: w.SampleID, Reason: "cross", Status: "open",
		}); err != nil {
			// One unqueueable sample must not cost the rest of the pass; the
			// next pass reaches it again and the others are waiting now.
			continue
		}
		opened++
	}
	if opened > 0 {
		log.Printf("compatibility: opened %d verification jobs for open dependency axes", opened)
	}
	return nil
}
