package serverstore

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// DependencyAxisWork is one coordinate whose dependency axis is open, and the
// verified sample whose re-verification would close it.
type DependencyAxisWork struct {
	Ecosystem string
	Name      string
	Version   string
	// SampleID is a sample that declares this coordinate and holds a passing
	// receipt. Re-verifying it is the act that reports the tree.
	SampleID string
	// Score is the demand behind this coordinate, weighted the way the
	// authoring queue already weights it: a chosen sighting counts
	// authoringDirectWeight carried ones. A dependency answer about a package
	// people run is worth more than one about a package nobody does.
	Score int64
}

// DependencyAxisStore lists coordinates whose dependency axis is open.
type DependencyAxisStore interface {
	// DependencyAxisOpen returns at most limit coordinates, most-wanted
	// first, whose dependency axis nothing has answered and whose sample can
	// answer it.
	//
	// maxAttempts bounds how many times one sample may be verified for this
	// reason. It is the convergence bound: a resolver that could not read
	// this tree three times will not read it on the fourth, and without a
	// ceiling the scheduler would re-open the same job forever and spend the
	// fleet on a question it has already declined to answer.
	DependencyAxisOpen(ctx context.Context, maxAttempts, limit int) ([]DependencyAxisWork, error)
}

// DependencyAxisMaxAttempts is how many verification jobs one sample may be
// given before the scheduler stops asking for its tree.
//
// Four, which is the ceiling cross verification already uses for a sample no
// peer can judge. The two bounds answer the same question -- how long does
// this network keep spending workers on a coordinate that will not resolve --
// and a second, different number would be a second policy nobody wrote down.
const DependencyAxisMaxAttempts = 4

// DependencyAxisPerPass bounds how many jobs one scheduler pass opens.
//
// The builder runs on the snapshot interval, so this is a rate rather than a
// total: the backlog drains over passes instead of arriving at the verifiers
// in one burst that buries the cross queue somebody else is waiting on.
const DependencyAxisPerPass = 50

// dependencyAxisAdmit applies the rules both stores must agree on: an
// ecosystem nobody here can read is not work, one sample is offered once
// however many coordinates it declares, and demand decides the order.
//
// The applicability rule is domain.DependencyNotApplicable -- the same
// sentence the census subtracts by and the /gaps page prints. Deriving it a
// second time in SQL is how the queue and the census drifted apart before
// (#119), so SQL selects the rows and this decides which of them are work.
func dependencyAxisAdmit(in []DependencyAxisWork, limit int) []DependencyAxisWork {
	sort.SliceStable(in, func(i, j int) bool {
		a, b := in[i], in[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Ecosystem != b.Ecosystem {
			return a.Ecosystem < b.Ecosystem
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.SampleID < b.SampleID
	})
	seen := make(map[string]bool, len(in))
	out := make([]DependencyAxisWork, 0, len(in))
	for _, w := range in {
		if w.SampleID == "" {
			continue
		}
		// "No scanner ships for this ecosystem" is a fact about this binary
		// rather than about the package: a Maven artifact has dependencies.
		// Handing a verifier a job whose deliverable nothing in the image can
		// produce is exactly the impossible work #87 asks the scheduler to
		// stop emitting.
		if _, na := domain.DependencyNotApplicable(w.Ecosystem); na {
			continue
		}
		// One verification reports the whole tree its resolver wrote, so a
		// sample declaring three open coordinates closes all three. A job per
		// coordinate would bill the fleet three times for one answer.
		if seen[w.SampleID] {
			continue
		}
		seen[w.SampleID] = true
		out = append(out, w)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// DependencyAxisOpen lists the coordinates whose dependency axis is open.
//
// The Fake reads the same four facts PostgreSQL does -- a passing sample
// declares the coordinate, no edge names it as a parent, no resolution
// recorded it as a leaf, and no verification for that sample is live or spent
// -- and hands them to the shared admission rule, so the two stores cannot
// disagree about what is work.
func (f *Fake) DependencyAxisOpen(_ context.Context, maxAttempts, limit int) ([]DependencyAxisWork, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resolved := f.resolvedParents()
	scores := f.packageDemand()
	attempts := make(map[string]int)
	live := make(map[string]bool)
	for _, j := range f.jobs {
		if j.Reason == "cross" {
			attempts[j.SampleID]++
		}
		if j.Status == "open" || j.Status == "claimed" {
			live[j.SampleID] = true
		}
	}
	var out []DependencyAxisWork
	for sampleID, sample := range f.samples {
		if sample.Quarantined {
			continue
		}
		passed := false
		for _, receipt := range f.receipts[sampleID] {
			if receipt.ContractResult == "PASS" {
				passed = true
				break
			}
		}
		if !passed {
			continue
		}
		// A sample already waiting on a verification is already asking the
		// question. A second job would not make the answer arrive sooner and
		// it takes a verifier away from a coordinate nobody is asking about.
		if live[sampleID] || (maxAttempts > 0 && attempts[sampleID] >= maxAttempts) {
			continue
		}
		var manifest struct {
			Packages []string `json:"packages"`
		}
		if json.Unmarshal([]byte(sample.ManifestJSON), &manifest) != nil {
			continue
		}
		for _, purl := range manifest.Packages {
			pkg, ok := f.packages[purl]
			if !ok || pkg.Version == "" || pkg.Publicness != "PUBLIC" {
				continue
			}
			if resolved[purl] || f.resolvedNone[[3]string{pkg.Ecosystem, pkg.Name, pkg.Version}] {
				continue
			}
			out = append(out, DependencyAxisWork{
				Ecosystem: pkg.Ecosystem, Name: pkg.Name, Version: pkg.Version,
				SampleID: sampleID, Score: scores[purl],
			})
		}
	}
	return dependencyAxisAdmit(out, limit), nil
}

// packageDemand is the weighted evidence behind each release, the same score
// the candidate query ranks sample work by. Caller holds f.mu.
func (f *Fake) packageDemand() map[string]int64 {
	out := make(map[string]int64)
	for observed, score := range f.merge.observations {
		if meta := f.aggMeta[observed]; meta != nil {
			score *= authoringChoiceWeight(meta.direct)
		}
		out[observed.PURL] += score
	}
	return out
}
