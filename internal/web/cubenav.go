package web

import (
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// ---------------------------------------------------------------------------
// The navigator: which rung of the drill the reader is standing on.
//
// A pinned URL used to arrive as a row of filter chips — "OS alpine musl",
// "패키지 버전 v1.6.0", "심볼 whole package" — which says what is fixed and
// says nothing about where that is. A reader could not tell whether four pins
// meant they were halfway down or standing at the bottom, so a coordinate with
// nothing below it looked exactly like one with a whole symbol axis left, and
// the only way to find out was to click and see.
//
// The rungs are the shape of the data, not of the URL:
//
//	package → version → environment/tool → symbol/API → sample/evidence
//
// A rung whose dimensions this package never recorded is left out rather than
// drawn as an empty step the reader can never satisfy. What is never left out
// is the answer to three questions: where am I, what is still below me, and is
// this the bottom.

// cubeNavDims maps each rung to the dimensions that decide it. The
// environment rung holds several because "which environment" is one question
// to a reader even though the evidence records it as six columns.
var cubeNavDims = map[string][]string{
	"version": {"version"},
	"env":     {"os", "libc", "arch", "runtime", "tool", "context"},
	"symbol":  {"symbol"},
}

// cubeNavOrder is the drill order the rungs are drawn in. "package" is the
// root and needs no dimension; "evidence" is the bottom and is not a
// dimension at all — it is what the coordinate finally holds.
var cubeNavOrder = []string{"package", "version", "env", "symbol", "evidence"}

// cubeNavStep is one rung.
type cubeNavStep struct {
	Key   string
	Label string
	// Value is what this rung has been narrowed to, in the data's own words.
	// Empty means the rung is still open.
	Value string
	// Href steps BACK to this rung: it keeps the pins of the rungs above and
	// drops this one and everything below, so going up never lands on a
	// coordinate the reader has to re-derive, and it carries the language the
	// page is being read in. Empty on the rung they are standing on, on rungs
	// below it, and wherever stepping back would change nothing.
	Href    string
	Current bool
	// Done marks a rung the coordinate has decided. A done rung behind the
	// current one is trail; the current one is where the next click acts.
	Done bool
}

// cubeNav is the whole strip plus the two facts a reader needs from it.
type cubeNav struct {
	Steps []cubeNavStep
	// Terminal reports that no rung is left to descend: this coordinate is
	// the bottom, and the page says so instead of leaving the reader to
	// discover it by clicking something that does not move.
	Terminal bool
	// NextLabel names the rung a click from here goes to, so a drill-down
	// affordance can say where it leads rather than only that it leads.
	NextLabel string
}

// buildCubeNav places the reader on the ladder.
//
// facts is the package's whole cube, not the current slice: which rungs exist
// is a fact about the package and must not change as the reader narrows —
// a symbol rung that vanished the moment a symbol was pinned would tell the
// reader they had left the ladder rather than reached its bottom.
func buildCubeNav(facts []cubeFact, coord, filters map[string]string,
	pagePath, name, lang string, terminal bool) cubeNav {

	present := func(dims []string) []string {
		var out []string
		for _, d := range dims {
			if len(cubeDimValues(facts, d)) > 0 {
				out = append(out, d)
			}
		}
		return out
	}
	// The pins of every rung strictly above index i. Stepping back up drops
	// what is below and keeps what is above, which is the only move that does
	// not throw away the reader's own narrowing.
	pinsAbove := func(i int) map[string]string {
		keep := map[string]string{}
		for j := 0; j < i; j++ {
			for _, d := range cubeNavDims[cubeNavOrder[j]] {
				if v := filters[d]; v != "" {
					keep[d] = v
				}
			}
		}
		return keep
	}

	var nav cubeNav
	// order[k] is the rung index in cubeNavOrder of the k-th rendered step,
	// which is what pinsAbove needs — skipped rungs must not shift it.
	var order []int
	current := -1
	for i, key := range cubeNavOrder {
		step := cubeNavStep{Key: key, Label: i18n.T(lang, "nav.step_"+key)}
		switch key {
		case "package":
			step.Value, step.Done = name, true
		case "evidence":
			// The bottom rung is reached, not chosen: it is decided exactly
			// when nothing above it is still open.
			step.Done = terminal
		default:
			dims := present(cubeNavDims[key])
			if len(dims) == 0 {
				// This package's evidence has no such dimension. Drawing the
				// rung anyway would promise a step that can never be taken.
				continue
			}
			var vals []string
			decided := true
			for _, d := range dims {
				v := coord[d]
				if v == "" {
					decided = false
					continue
				}
				if key == "symbol" && v == cubePackageLevel && name != "" {
					v = name
				}
				vals = append(vals, v)
			}
			step.Value, step.Done = strings.Join(vals, " · "), decided
		}
		if !step.Done && current < 0 {
			step.Current = true
			current = len(nav.Steps)
		}
		nav.Steps = append(nav.Steps, step)
		order = append(order, i)
	}
	if len(nav.Steps) == 0 {
		return nav
	}
	// Nothing open above the bottom rung: the coordinate is decided and the
	// evidence is all that is left to look at.
	if current < 0 {
		current = len(nav.Steps) - 1
		nav.Steps[current].Current = true
	}
	// Only the rungs BEHIND the reader are links, and only where stepping
	// back would actually change the slice. A step that rebuilds the URL the
	// reader is already on is a link that goes nowhere, which is the whole
	// complaint this navigator exists to answer.
	seen := map[string]bool{}
	for k := 0; k < current; k++ {
		q := pinsAbove(order[k])
		if sameFilterSet(q, filters) {
			continue
		}
		href := cubeHref(pagePath, cubeQuery(q, "", "", lang))
		if seen[href] {
			continue
		}
		seen[href] = true
		nav.Steps[k].Href = href
	}
	nav.Terminal = terminal
	if !terminal {
		nav.NextLabel = nav.Steps[current].Label
	}
	return nav
}

// sameFilterSet reports whether two pin sets pin the same things — the test
// for "stepping back here lands on the page we are on".
func sameFilterSet(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
