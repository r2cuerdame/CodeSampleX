package web

import "github.com/r2cuerdame/codesamplex/internal/web/i18n"

// ---------------------------------------------------------------------------
// One mark, and its colour is the whole state.
//
// The grid used to carry two of them side by side: a document for "there is a
// sample for this release and API" and a diamond for "this network ran its own
// contract in THIS environment". Both facts are real and they are still keyed
// differently — that separation is in cubecode.go and it has not moved — but
// showing them as two glyphs published the internal model (Sample here,
// Evidence there) as the reader's vocabulary. In use, nobody wanted two
// answers. They wanted one:
//
//	a document means there is a sample here, and its colour is how it ran here.
//
// So the document is the only user-facing mark, and the diamond is gone rather
// than demoted: a second glyph in the same cell is the thing that failed.
//
//	sampleNone     no document        nothing published, nothing run
//	sampleUnknown  grey document      a sample, no run of ours recorded here
//	samplePass     green document     our run of it passed here
//	sampleFail     red document       our run of it failed here
//	sampleMixed    split document     both outcomes are recorded here
//
// Mixed is split rather than averaged. A coordinate that passed twice and
// failed once is not "mostly fine", and collapsing it to one colour throws
// away the half a reader most needs to see.
type sampleState string

const (
	sampleNone    sampleState = ""
	sampleUnknown sampleState = "unknown"
	samplePass    sampleState = "pass"
	sampleFail    sampleState = "fail"
	sampleMixed   sampleState = "mixed"
)

// deriveSampleState reads the state off the evidence and never off the UI.
//
// published is how many samples answer this release and API (codeIndex, which
// takes no environment argument). ranPass and ranFail are OUR OWN runs at this
// exact coordinate — verifications, never observations. A build somebody else
// reported says a project containing this package compiled; it does not say a
// sample of ours ran, and letting it colour the document would put a claim on
// the page that nothing executed to support.
//
// A recorded run outranks the published aggregate for EXISTENCE too: if a
// contract of ours ran here, a sample was here, whatever a bounded aggregate
// happens to know about it. The alternative is a coordinate that reports a
// failure and draws no mark to hang it on.
func deriveSampleState(published, ranPass, ranFail int64) sampleState {
	switch {
	case ranPass > 0 && ranFail > 0:
		return sampleMixed
	case ranPass > 0:
		return samplePass
	case ranFail > 0:
		return sampleFail
	case published > 0:
		return sampleUnknown
	}
	return sampleNone
}

// sampleStateKey is the locale key for a state's one sentence.
//
// One sentence per state, used by the cell's accessible name, the same cell's
// tooltip, the answer card's chip and the legend — so a reader who learns the
// mark with a mouse, with a screen reader or from the legend learns the same
// thing. sampleNone has a sentence too: "there is nothing here" is an answer,
// and a legend that lists only marks cannot give it.
func sampleStateKey(s sampleState) string {
	switch s {
	case samplePass:
		return "sample.pass"
	case sampleFail:
		return "sample.fail"
	case sampleMixed:
		return "sample.mixed"
	case sampleUnknown:
		return "sample.unknown"
	}
	return "sample.none"
}

func sampleStateLabel(lang string, s sampleState) string {
	return i18n.T(lang, sampleStateKey(s))
}

// sampleStates is the order the legend teaches them in: absent, then present
// but unrun, then the two outcomes, then both at once.
var sampleStates = []sampleState{sampleNone, sampleUnknown, samplePass, sampleFail, sampleMixed}

// labelSampleMarks writes the page-language sentence onto every mark in a
// grid.
//
// buildPivotCell decides the state and knows no language; every page that
// renders a grid knows one. Keeping the two apart is why a cell built for the
// landing page and a cell built for a package page cannot end up saying
// different things about the same evidence.
func labelSampleMarks(g *pivotGrid, lang string) {
	g.StatPass = i18n.T(lang, "grid.cells_pass")
	g.StatFail = i18n.T(lang, "grid.cells_fail")
	g.StatMixed = i18n.T(lang, "grid.cells_mixed")
	g.StatObserved = i18n.T(lang, "grid.cells_observed")
	for ri := range g.Rows {
		for ci := range g.Rows[ri].Cells {
			cell := &g.Rows[ri].Cells[ci]
			if cell.Sample == sampleNone {
				cell.SampleLabel = ""
				continue
			}
			cell.SampleLabel = sampleStateLabel(lang, cell.Sample)
		}
	}
}
