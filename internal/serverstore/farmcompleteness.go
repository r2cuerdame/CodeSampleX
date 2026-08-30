package serverstore

import "github.com/r2cuerdame/codesamplex/internal/domain"

import "context"

// The production unit is not "a coordinate with no sample". R2C-126: it is
// which of three assets a coordinate has -- a reusable sample, evidence that
// somebody ran it, and a resolved dependency graph -- and the work that
// remains is whichever of the three is missing.
//
// The panel counted one of them. A release with a sample and no resolved
// graph read as finished, so the fleet could be told there was nothing left
// while two thirds of the corpus was incomplete on an axis nothing printed.
// Measured against production on 2026-08-23 over 2,880 PUBLIC releases:
//
//	SED  166   all three
//	SE-  881   sample and evidence, no resolved graph
//	-ED   47   evidence and a graph, no sample
//	-E-  1763  evidence alone
//	S--    6   a sample and nothing else
//	---   17   nothing
//
// S-D and --D were zero, and that is a property of where the data comes from
// rather than an accident: the only thing that records a resolved graph today
// is an observation batch, and a batch carries the package it is about, so a
// coordinate with a graph has evidence by construction. Both cells are still
// reported, because the day a resolution arrives from a verification instead
// of a scan they stop being zero and a shape that had dropped them would hide
// it.

// dependencyState is what the network knows about one coordinate's resolved
// dependencies.
//
// The three are kept apart because two of them are routinely confused and
// only one of the two is a fact. "This release pulls nothing" is a measured
// answer; "nobody has resolved this release" is silence. R2C-108 renders this
// axis, and printing the second as the first would be the network asserting
// something it never measured -- the exact failure this project exists to
// refuse.
type dependencyState int

const (
	// dependencyUnknown: nothing has resolved this release. NOT "it has no
	// dependencies".
	dependencyUnknown dependencyState = iota
	// dependencyGraph: a resolution named this release's children.
	dependencyGraph
	// dependencyProvenNone: a resolution ran against this release and found
	// no children. Nothing populates this yet -- see FarmCompleteness.
	dependencyProvenNone
)

// completenessStates are the eight cells, in the order the matrix reads.
// Every cell is always present, so a zero is visibly zero rather than absent.
var completenessStates = [8]string{"SED", "SE-", "S-D", "S--", "-ED", "-E-", "--D", "---"}

// completenessKey renders one coordinate's three axes as a cell name.
func completenessKey(sample, evidence bool, dependency dependencyState) string {
	key := []byte("---")
	if sample {
		key[0] = 'S'
	}
	if evidence {
		key[1] = 'E'
	}
	if dependency != dependencyUnknown {
		key[2] = 'D'
	}
	return string(key)
}

// FarmCompleteness is every verifiable coordinate counted by which of the
// three assets it holds.
type FarmCompleteness struct {
	// States is the eight-cell matrix, keyed by completenessStates. Always
	// eight entries.
	States map[string]int
	// The dependency axis split three ways. These sum to the same total as
	// States, because every coordinate is in exactly one of each.
	//
	// ProvenNone is reported separately from Unknown even while nothing can
	// produce it. A field that is absent until it has a value is a field
	// nobody notices arriving, and this is the one number that says whether
	// the network can tell "no dependencies" from "we never looked".
	DependencyGraph      int
	DependencyProvenNone int
	DependencyUnknown    int
	// What this network cannot produce here, counted apart from what it has
	// not produced yet.
	//
	// The census used to count both as missing. Measured on production: 398
	// npm per-platform native builds and one Gradle plugin marker sat inside
	// the 1,372 releases reported as having no sample, and the authoring
	// queue declined every one of them on every poll — the queue's judgement
	// and the backlog's denominator disagreed by 399 coordinates. 507 more
	// were reported as having no dependency graph in ecosystems where no
	// scanner ships, so nothing could ever produce one.
	//
	// A scheduler built on that denominator hands out work nobody can close,
	// which is the failure #87 was opened for. These are subtracted from the
	// eight states rather than hidden: a coordinate counted here is not in
	// States at all on that axis.
	SampleNotApplicable     int
	DependencyNotApplicable int
}

// newFarmCompleteness returns a matrix with all eight cells at zero.
func newFarmCompleteness() FarmCompleteness {
	states := make(map[string]int, len(completenessStates))
	for _, state := range completenessStates {
		states[state] = 0
	}
	return FarmCompleteness{States: states}
}

// FarmCompletenessStore counts the corpus by three-axis completeness. It is
// the stock the scheduler is judged against: NO_WORK is only honest when
// every cell but SED is empty of coordinates the current policy can run.
type FarmCompletenessStore interface {
	FarmCompletenessNow(ctx context.Context) (FarmCompleteness, error)
}

// add folds one group of coordinates into the census, holding the eight
// states to what this network can actually produce.
//
// A coordinate no sample can be written for is counted as
// SampleNotApplicable, and one whose ecosystem has no dependency scanner as
// DependencyNotApplicable. A coordinate unaskable on both axes leaves States
// entirely: States is the backlog, and an unaskable coordinate is not backlog.
//
// Both stores call this, so the Fake and PostgreSQL cannot drift on the one
// judgement the scheduler will be built on.
func (f *FarmCompleteness) add(state, ecosystem, name string, n int) {
	sampleNA := false
	if _, na := domain.SampleNotApplicable(ecosystem, name); na {
		sampleNA = true
	}
	_, depNA := domain.DependencyNotApplicable(ecosystem)

	if sampleNA {
		f.SampleNotApplicable += n
	}
	if depNA {
		f.DependencyNotApplicable += n
	}
	// A coordinate unaskable on BOTH axes is not in the backlog at all.
	if sampleNA && depNA {
		return
	}
	f.States[state] += n
	switch {
	case depNA:
		// Not counted as unknown: nobody can look, so "we never looked" would
		// read as a gap somebody could close.
	case state[2] == 'D':
		f.DependencyGraph += n
	default:
		f.DependencyUnknown += n
	}
}
