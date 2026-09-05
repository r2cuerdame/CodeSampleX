package httpapi

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A missing-axis kind has to survive the whole handout path, not just the
// query that produced it. authoringCandidateEligible refuses any kind it does
// not know by name, so a candidate the scheduler emits and the handler drops
// is a queue that reports NO_WORK with work in its own snapshot -- the exact
// failure #87 is about, reintroduced one layer higher.
func TestEvidenceAxisWorkReachesTheWorker(t *testing.T) {
	store := newSnapshotStore(serverstore.WantedRow{
		Ecosystem: "npm", Name: "unseen", Version: "1.0.0", Kind: "EVIDENCE",
	})
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Store = store })

	if kind := pollOnce(t, srv.URL, store, 1); kind != "EVIDENCE" {
		t.Fatalf("poll handed out %q, want EVIDENCE: the evidence axis is offered and then dropped", kind)
	}
}

// The rules that refuse work nobody can close apply to every kind, including
// the new ones. An npm per-platform native build has no importable surface at
// all, so offering it under a different kind would only rename the impossible
// job the queue already declines on every poll.
func TestEvidenceAxisRespectsTheSameApplicabilityRules(t *testing.T) {
	store := newSnapshotStore(serverstore.WantedRow{
		Ecosystem: "npm", Name: "@tailwindcss/oxide-linux-x64-gnu", Version: "1.0.0", Kind: "EVIDENCE",
	})
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Store = store })

	if kind := pollOnce(t, srv.URL, store, 1); kind != "" {
		t.Fatalf("poll handed out %q for a per-platform native build; the sample would import a .node binary its parent selects", kind)
	}
}
