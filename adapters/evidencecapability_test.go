package adapters

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The queue may not hand out Evidence work in an ecosystem nothing here can
// observe, and the site may not say an observer is missing when one ships.
//
// domain.EvidenceNotApplicable decides whether a release's evidence axis is
// "nobody has run it yet" or "nothing here can ever record a run", and the
// candidate snapshot, the claim gate, the census and /gaps all read that
// answer. It is a hardcoded list, for the same layering reason as the
// dependency taxonomy, and a hardcoded list beside a real capability drifts
// silently.
//
// The capability it has to track is A1 -- build/typecheck/test observation --
// which is exactly what `csx run` records. pub had A4 alone and was handed
// Evidence work anyway (#387); a Flutter lease ran resolve/build to
// completion and produced no observation because no pub adapter ships.
func TestTheEvidenceTaxonomyMatchesTheRegisteredAdapters(t *testing.T) {
	observes := map[string]bool{}
	for _, a := range All() {
		if slices.Contains(a.Capabilities(), "A1") {
			observes[a.Ecosystem()] = true
		}
	}
	if len(observes) == 0 {
		t.Fatal("no registered adapter claims A1; the check has nothing to compare")
	}

	var wrong []string
	for eco := range observes {
		if reason, notApplicable := domain.EvidenceNotApplicable(eco); notApplicable {
			wrong = append(wrong, eco+": ships an A1 observer, but the taxonomy says "+reason)
		}
	}
	for _, a := range All() {
		eco := a.Ecosystem()
		if observes[eco] {
			continue
		}
		if _, notApplicable := domain.EvidenceNotApplicable(eco); !notApplicable {
			wrong = append(wrong, eco+": claimed observable, but no registered adapter claims A1")
		}
	}
	for _, eco := range domain.EvidenceObservableEcosystems() {
		if !observes[eco] {
			wrong = append(wrong, eco+": claimed observable, but no registered adapter claims A1")
		}
	}
	sort.Strings(wrong)
	if len(wrong) > 0 {
		t.Errorf("the evidence taxonomy and the adapters disagree:\n  %s", strings.Join(wrong, "\n  "))
	}
}

// A verifier-only ecosystem is exactly the shape #387 found: the published
// matrix records A4 and nothing else, so it must be unaskable on the evidence
// axis, and the sample axis must stay open because the sandbox proves a
// sample without a scanner.
func TestVerifierOnlyEcosystemsAreUnaskableForEvidence(t *testing.T) {
	doc := loadMatrix(t)
	for _, entry := range doc.Adapters {
		hasScanner := false
		for _, c := range entry.Capabilities {
			if c != "A4" {
				hasScanner = true
			}
		}
		if hasScanner {
			continue
		}
		reason, na := domain.EvidenceNotApplicable(entry.Ecosystem)
		if !na {
			t.Errorf("%s publishes a verifier-only adapter but is counted as evidence-observable", entry.Ecosystem)
		}
		if !strings.Contains(reason, entry.Ecosystem) {
			t.Errorf("%s: the reason must name the ecosystem so a reader knows which lane is missing, got %q", entry.Ecosystem, reason)
		}
		if _, sampleNA := domain.SampleNotApplicable(entry.Ecosystem, "any"); sampleNA {
			t.Errorf("%s: a verifier-only ecosystem still proves samples; the sample axis must stay askable", entry.Ecosystem)
		}
	}
}
