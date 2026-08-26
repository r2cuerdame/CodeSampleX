package web

import (
	"time"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// ---------------------------------------------------------------------------
// The answer, stated before the instrument that finds it.
//
// A reader arriving on a package page has a question — does this API, at this
// version, in this environment, work, and how much evidence is there — and the
// page used to open with the tool for asking it. On an exact link that is the
// wrong way round twice over: the coordinate is already decided, so there is
// nothing left to explore, and the only thing on screen speaking about the
// coordinate was a grid cell in the compressed notation a browse grid needs
// (glyph, percentage, denominator). That notation is right for scanning forty
// cells and wrong for reading one: it asks the reader to learn a legend before
// they can learn an outcome.
//
// So when the drill-down bottoms out, the page states the coordinate once, in
// words, with the counts each basis rests on beside it, and the cube below it
// becomes what it actually is at that point — the way to compare a DIFFERENT
// version or environment.

// cubeAnswerEnvDims is the environment part of a coordinate, in the order the
// exact records already print it. Keeping one order means the answer and the
// record underneath cannot disagree about how to say the same place.
var cubeAnswerEnvDims = []string{"os", "libc", "arch", "runtime", "tool", "context"}

// cubeAnswerFact is one named piece of evidence under the headline.
type cubeAnswerFact struct {
	Label string
	Value string
	// Absent marks "nothing recorded at this coordinate". It is a real
	// answer and it is printed rather than hidden — a missing row reads as
	// an oversight, and the difference between "we never ran this here" and
	// "we ran it and it failed" is the whole point of the two bases — but it
	// is drawn quieter so it cannot be mistaken for a count.
	Absent bool
}

// cubeAnswer is the expanded result for one decided coordinate.
type cubeAnswer struct {
	// Symbol/Version/Env state the coordinate ONCE. Everything below the
	// card speaks about the same place and must not repeat it.
	Symbol  string // the package's own name for package-level evidence
	Version string
	Env     string
	// Basis is "verified" or "observed" — who ran what the headline counts.
	// BasisNote says it in the reader's language; the grid's tooltip cannot,
	// because it is assembled from English data phrases.
	Basis     string
	BasisNote string
	Glyph     string
	Tone      string
	// Headline is the one line the whole page exists to deliver.
	Headline string
	Facts    []cubeAnswerFact
	// SymbolNote qualifies the coordinate's API where the API is not one.
	// "whole package" is a package-level TOTAL, disjoint from the symbols
	// beside it, and printing the package's name in the symbol slot made it
	// read as an API the package exports.
	SymbolNote string
	// Code is how many published samples answer this release and API,
	// whatever environment the coordinate names. CodeLabel says so in words;
	// NoCodeLabel is set instead when there are none, because "no code yet"
	// is an answer and an absent affordance is not.
	Code        int64
	CodeLabel   string
	NoCodeLabel string
	// CodeUnknownLabel is distinct from NoCodeLabel: a failed aggregate read
	// cannot support a public absence claim.
	CodeUnknownLabel string
	// EnvLabel says whether THIS environment was verified, which is the other
	// half of the pair the old single mark conflated. The template renders it
	// only where the coordinate actually NAMES an environment: with none
	// decided, "verified in this environment" points at nothing.
	EnvLabel string
	// Terminal marks the bottom of the drill. The card is built at the bottom
	// and nowhere else, so this is always true where it is set; the WORDING
	// lives on the navigator, which is the instrument that talks about depth,
	// and printing it in both places said the same sentence twice on one
	// screen.
	Terminal bool
	// Actions are the ways OUT of the instrument to real evidence. Each one
	// is here only because the thing it opens exists: an action that leads to
	// an empty page is the dead end this card was built to remove.
	Actions []cubeAction
}

// cubeAction is one evidence destination at a terminal coordinate.
//
// It is deliberately not a bare coordinate wearing the drill-down's blue.
// Every link inside the cube narrows the slice; these leave it, so they carry
// a label saying what they open and are drawn as buttons rather than as more
// of the instrument (the third visual grammar: filter, navigator, evidence).
type cubeAction struct {
	Kind  string // "samples" | "records" | "failures" | "deps"
	Label string
	Href  string
}

// cubeAnswerEnv joins the environment dimensions a coordinate has decided.
func cubeAnswerEnv(coord map[string]string) string {
	var parts []string
	for _, dim := range cubeAnswerEnvDims {
		if v := coord[dim]; v != "" {
			parts = append(parts, v)
		}
	}
	return joinDims(parts)
}

// cubeResultLine states an aggregate's outcome in words, in the page
// language, and names the basis it is counting.
//
// It is the one place the leaf's wording is decided. pivotCell.Tip is not a
// candidate: it is built from English phrases glued to data values ("3
// observations · last seen 2026-08-12 · cross-checked"), which is fine as a
// tooltip on a grid of data and became visible body text on the Korean exact
// record, where the only sentence a reader got was in a language the rest of
// the page was not in.
func cubeResultLine(a *pivotAgg, lang string) (headline, basisNote, basis string) {
	if a == nil {
		return "", "", ""
	}
	n := func(v int64) string { return i18n.FormatInt(lang, v) }
	obs := a.obsPass + a.obsFail
	ver := a.verPass + a.verFail
	switch {
	case ver > 0:
		return i18n.T(lang, "answer.passed_of", n(a.verPass), n(ver)),
			i18n.T(lang, "answer.basis_verified"), "verified"
	case obs > 0:
		return i18n.T(lang, "answer.passed_of", n(a.obsPass), n(obs)),
			i18n.T(lang, "answer.basis_observed"), "observed"
	case a.used > 0:
		// Presence is not a run. The pass rate deliberately excludes usage
		// records, so a coordinate with only usage has no rate at all and
		// must not borrow one.
		return i18n.T(lang, "answer.usage_only"), i18n.T(lang, "answer.basis_observed"), "observed"
	}
	return "", "", ""
}

// buildCubeAnswer states one decided coordinate. nil when the slice carries
// nothing to state.
func buildCubeAnswer(sliced []cubeFact, coord map[string]string,
	eco, name, lang string, now time.Time, code *codeIndex) *cubeAnswer {

	if len(sliced) == 0 {
		return nil
	}
	agg := mergeCubeFacts(sliced)
	headline, basisNote, basis := cubeResultLine(agg, lang)
	if headline == "" {
		return nil
	}
	// The mark and the colour come from the same builder the grid uses, so
	// an expanded record and the cell it drilled from can never disagree.
	cell := buildPivotCell(agg, now)

	ans := &cubeAnswer{
		Symbol:    coord["symbol"],
		Version:   coord["version"],
		Env:       cubeAnswerEnv(coord),
		Basis:     basis,
		BasisNote: basisNote,
		Glyph:     cell.Glyph,
		Tone:      cell.Tone,
		Headline:  headline,
		// The card is built at the bottom of the drill and nowhere else, so
		// its existence IS the terminal fact.
		Terminal: true,
	}
	// "whole package" is what the evidence IS, and a reader standing on the
	// package already knows which package that is. The note is what the name
	// alone stopped saying: this row is the release's TOTAL, not an API.
	//
	// Only for the package-level value, never for an undecided symbol. An
	// undecided slice covers several APIs and is not the package-level total;
	// labelling it as one states a coordinate the reader has not reached.
	if ans.Symbol == cubePackageLevel {
		if name != "" {
			ans.Symbol = name
		}
		ans.SymbolNote = i18n.T(lang, "cube.package_aggregate")
	} else if ans.Symbol == "" && name != "" {
		ans.Symbol = name
	}

	n := func(v int64) string { return i18n.FormatInt(lang, v) }
	ratio := func(pass, total int64) string { return n(pass) + " / " + n(total) }
	add := func(label, value string, absent bool) {
		ans.Facts = append(ans.Facts, cubeAnswerFact{Label: label, Value: value, Absent: absent})
	}
	none := i18n.T(lang, "answer.none_here")

	obs := agg.obsPass + agg.obsFail
	ver := agg.verPass + agg.verFail
	// Both bases, always, including the empty one. In production every
	// verified coordinate has zero observations and most observed ones have
	// zero verifications, and printing only the side that has numbers left
	// the reader unable to tell which side that was.
	if obs > 0 {
		add(i18n.T(lang, "answer.observations"), ratio(agg.obsPass, obs), false)
	} else {
		add(i18n.T(lang, "answer.observations"), none, true)
	}
	if ver > 0 {
		add(i18n.T(lang, "answer.verifications"), ratio(agg.verPass, ver), false)
	} else {
		add(i18n.T(lang, "answer.verifications"), none, true)
	}
	if agg.used > 0 {
		add(i18n.T(lang, "answer.usage"), n(agg.used), false)
	}
	if agg.obsPeers > 0 {
		add(i18n.T(lang, "answer.peers"), n(agg.obsPeers), false)
	}
	// Observation evidence is co-occurrence: a failure without a normalized
	// fingerprint says a
	// build CONTAINING this package broke, not that this package broke. The
	// grid carries that caveat in its tooltip and the expanded record has to
	// carry it too, or the number reads as stronger here than there.
	if agg.obsFail > 0 && agg.obsAttributed < agg.obsFail {
		add(i18n.T(lang, "answer.attributed"), ratio(agg.obsAttributed, agg.obsFail), false)
	}
	if agg.cross {
		add(i18n.T(lang, "answer.cross"), i18n.T(lang, "answer.cross_value"), false)
	}
	lastSeen := agg.verLastSeen
	if ver == 0 {
		lastSeen = agg.obsLastSeen
	}
	if d := datePart(lastSeen); d != "" {
		add(i18n.T(lang, "answer.last_seen"), d, false)
	}

	// The two states the old single mark ran together, now stated apart.
	//
	// Code availability is keyed by release and API and is true or false
	// wherever the reader is standing; verification is keyed by this exact
	// environment. A sample the fleet ran only on Linux is still the code
	// that exists for a Windows reader, and the Windows column is still
	// unverified — both sentences are true at once and the page says both.
	version, symbol := coord["version"], coord["symbol"]
	ans.Code = code.at(version, symbol)
	switch {
	case code == nil || !code.known:
		ans.CodeUnknownLabel = i18n.T(lang, "cube.code_unknown")
	case ans.Code > 0:
		ans.CodeLabel = i18n.T(lang, "cube.code_yes")
	default:
		ans.NoCodeLabel = i18n.T(lang, "cube.code_none")
	}
	if ver > 0 {
		ans.EnvLabel = i18n.T(lang, "cube.env_verified")
	} else {
		ans.EnvLabel = i18n.T(lang, "cube.env_unverified")
	}

	// The ways out of the instrument, each one offered only because what it
	// opens is there. An affordance that lands on an empty page is the dead
	// end the reader complained about, and it costs more trust than the click
	// it saved.
	if version != "" {
		listHref := versionHref(eco, name, version)
		if symbol != "" && symbol != cubePackageLevel {
			listHref = symbolHref(eco, name, version, symbol)
		}
		if ans.Code > 0 {
			// To the page that LISTS them, never to one sample. Each page on
			// the way down narrows by one dimension — the package counts
			// answers per release, the release counts them per API, the API
			// lists them — and a package page that linked a sample directly
			// would put uuid's ninety-six back in one pile.
			href := listHref
			ans.Actions = append(ans.Actions, cubeAction{
				Kind:  "samples",
				Label: i18n.T(lang, "answer.action_samples", i18n.FormatInt(lang, ans.Code)),
				Href:  href,
			})
		}
		// The measured record for the coordinate: the matrix and the receipts
		// behind the counts above. It is not labelled "samples" any more —
		// that promised code on coordinates that have none, which is how a
		// reader ended up on a page with nothing to read.
		ans.Actions = append(ans.Actions, cubeAction{
			Kind:  "records",
			Label: i18n.T(lang, "answer.action_records"),
			Href:  listHref,
		})
	}
	return ans
}

// addAction appends one evidence destination, ignoring a nil card and a
// destination the page has nothing to put behind.
func (a *cubeAnswer) addAction(kind, label, href string) {
	if a == nil || href == "" || label == "" {
		return
	}
	a.Actions = append(a.Actions, cubeAction{Kind: kind, Label: label, Href: href})
}

// dropSharedCoordinate blanks the parts of each exact record the answer card
// above it already states.
//
// An exact URL pins version, symbol, OS and runtime; the card names them, the
// record named them again and the control bar showed them a third time. Where
// several records remain, what tells them apart is what the coordinate did
// NOT decide — so that is what they keep.
func dropSharedCoordinate(rows []cubeLeafRow, ans *cubeAnswer) []cubeLeafRow {
	if ans == nil {
		return rows
	}
	for i := range rows {
		if ans.Version != "" && rows[i].Version == ans.Version {
			rows[i].Version = ""
		}
		if ans.Symbol != "" && rows[i].Symbol == ans.Symbol {
			rows[i].Symbol = ""
		}
		if ans.Env != "" && rows[i].Env == ans.Env {
			rows[i].Env = ""
		}
	}
	return rows
}
