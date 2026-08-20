package web

import (
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// ---------------------------------------------------------------------------
// The cube explorer view: one slice of a package's compatibility cube,
// rendered as an X × Y grid with the remaining pinned dimensions shown as
// removable filter chips. Clicking a cell pins one more value per axis and
// drills into the next slice; when at most one measured combination
// remains, the page renders the exact-contract leaf instead of a grid.

type cubeAxisOption struct {
	Key      string // dimension key ("os")
	Label    string // translated label
	Selected bool
}

type cubeChip struct {
	Dim        string
	DimLabel   string
	Value      string // data value, never translated
	RemoveHref string
}

// cubeFilterOption is one selectable value of a filter dropdown; the
// empty value means "every value of this dimension".
type cubeFilterOption struct {
	Value    string // "" = all; otherwise the recorded data value
	Label    string // data value, or the translated "all" label
	Selected bool
}

// cubeFilterSelect narrows the slice by one dimension, independently of
// which two dimensions are currently spread across the axes.
type cubeFilterSelect struct {
	// Fixed marks a dimension with exactly one value and no pin: it is
	// already decided, so the control shows that value and nothing else.
	Fixed   bool
	Dim     string
	Label   string
	Options []cubeFilterOption
	Active  bool
}

// cubeLeafRow is one measured combination at the bottom of a drill-down.
type cubeLeafRow struct {
	Version    string
	Symbol     string // cubePackageLevel for package-level evidence
	SymbolHref string // "" when the row is package-level
	// VersionHref is the way down. The leaf is where the drill-down bottoms
	// out, and it used to emit a symbol link and nothing else — nothing at all
	// for a package-level row — so the most engaged reader on the site arrived
	// at the deepest node with no way further. The version page below it is
	// the one that lists contract records.
	VersionHref string
	Env         string // remaining recorded dimensions, " · " joined
	Cell        pivotCell
}

type cubeView struct {
	X, Y string
	// XLabel/YLabel name the spread axes in the page language, so the grid
	// says what it is a map of without reading the pickers.
	XLabel, YLabel string
	// SwapHref transposes the grid — the same slice read the other way,
	// which is one click instead of two dropdown changes.
	SwapHref string
	XOptions []cubeAxisOption
	YOptions []cubeAxisOption
	// Filters narrow the cube by any dimension, whether or not it is on an
	// axis. They render as dropdowns beside the axis pickers.
	Filters    []cubeFilterSelect
	Chips      []cubeChip
	Grid       pivotGrid
	Leaf       []cubeLeafRow
	Action     string // GET form action, anchored so the reload keeps the grid in view
	Lang       string // hidden lang input value; "" for the default locale
	ClearHref  string
	HasFilters bool
	// NoMatch is set when pinned filters exclude every fact — a stale link
	// renders an honest empty state rather than a fabricated grid.
	NoMatch bool
	// WindowNote is set when the cube's assembly window (newest versions,
	// first symbols) dropped snapshots, so an empty cell must not read as
	// "never measured anywhere".
	WindowNote bool
	// Coord is the coordinate the slice has been narrowed to: every
	// dimension down to one value, pinned or not. Decided reports that
	// NOTHING is left to choose, which is when the page can speak about
	// this exact place rather than about the package in general.
	Coord   map[string]string
	Decided bool
}

// cubeCoord reads the coordinate out of a slice.
//
// A pinned dimension is decided because the reader said so; a dimension with
// exactly one value is decided because the evidence leaves no alternative.
// Both are the same fact to a reader, and treating the second as undecided
// asked for a click that could only ever pick what was already on screen.
func cubeCoord(sliced []cubeFact, filters map[string]string) map[string]string {
	coord := map[string]string{}
	for _, dim := range cubeDimKeys {
		if v := filters[dim]; v != "" {
			coord[dim] = v
			continue
		}
		if vals := cubeDimValues(sliced, dim); len(vals) == 1 {
			coord[dim] = vals[0]
		}
	}
	return coord
}

// cubeCoordDecided reports that no dimension still offers a choice — the
// bottom of the drill-down, and the only place the page can name one
// release's dependencies or one environment's failures without guessing
// which release or which environment the reader meant.
func cubeCoordDecided(sliced []cubeFact) bool {
	for _, dim := range cubeDimKeys {
		if len(cubeDimValues(sliced, dim)) >= 2 {
			return false
		}
	}
	return true
}

// cubeFilterBar builds the control bar for the dimensions not on an axis.
//
// It reads the whole slice, not what the grid renders. A symbol axis drops
// the package-level aggregate, but that fact still carries a real version, OS
// and package manager — hasown 2.0.3 was measured only at package level, and
// building the bar from the grid's evidence took 2.0.3 out of the version
// list entirely, so the reader could not select a version the package has.
//
// A dimension on an axis needs no dropdown — every value already has its own
// row or column, and picking one there collapses the axis to a single line,
// which reads as the grid breaking rather than as a filter working.
func cubeFilterBar(facts []cubeFact, x, y string, filters map[string]string, lang string) []cubeFilterSelect {
	var out []cubeFilterSelect
	for _, dim := range cubeDimKeys {
		if dim != "" && (dim == x || dim == y) {
			continue
		}
		if sel, ok := cubeFilterFor(facts, dim, filters, lang); ok {
			out = append(out, sel)
		}
	}
	return out
}

// cubeFilterFor builds one dimension's control from the slice narrowed by
// the OTHER pins, so switching a filter can never offer a combination the
// network has no evidence for.
//
// A dimension with one value gets a control too, fixed to that value. It used
// to be dropped on the reasoning that a control with no alternative filters
// nothing — but a missing control does not read as "decided", it reads as
// "not measured", and a reader standing on the only OS there is could not
// tell the difference. Fixed, the bar states the coordinate.
func cubeFilterFor(facts []cubeFact, dim string, filters map[string]string, lang string) (cubeFilterSelect, bool) {
	rest := map[string]string{}
	for d, v := range filters {
		if d != dim {
			rest[d] = v
		}
	}
	values := cubeDimValues(filterCubeFacts(facts, rest), dim)
	if len(values) == 0 && filters[dim] == "" {
		return cubeFilterSelect{}, false
	}
	sel := cubeFilterSelect{
		Dim:    dim,
		Label:  i18n.T(lang, "cube.dim_"+dim),
		Active: filters[dim] != "",
	}
	// Fixed on the count of values MEASURED, not of entries offered: the OS
	// control adds whole platforms beside exact environments, so a slice
	// measured only on alpine renders "linux" and "alpine musl" and counting
	// entries read that as a choice between a group and its only member.
	sel.Fixed = len(values) == 1 && filters[dim] == ""
	// The OS offers whole platforms as well as exact environments: "does it
	// run on Linux at all" and "does it run on alpine musl" are different
	// questions and a reader arrives with both. Decided, there is only the
	// environment itself to state.
	choices := make([]cubeFilterOption, 0, len(values))
	if dim == "os" && !sel.Fixed {
		choices = cubeOSFilterOptions(values)
	} else {
		for _, v := range values {
			choices = append(choices, cubeFilterOption{Value: v, Label: v})
		}
	}
	if !sel.Fixed {
		sel.Options = append(sel.Options, cubeFilterOption{
			Label: i18n.T(lang, "cube.all"), Selected: filters[dim] == "",
		})
	}
	for _, c := range choices {
		c.Selected = sel.Fixed || filters[dim] == c.Value
		sel.Options = append(sel.Options, c)
	}
	return sel, true
}

// parseCubeFilters reads the pinned dimensions from ?f_<dim>= parameters.
func parseCubeFilters(q url.Values) map[string]string {
	filters := map[string]string{}
	for _, dim := range cubeDimKeys {
		if v := q.Get("f_" + dim); v != "" {
			filters[dim] = v
		}
	}
	return filters
}

// cubeQuery rebuilds the explorer query string from filters + axes.
func cubeQuery(filters map[string]string, x, y, lang string) url.Values {
	q := url.Values{}
	for dim, v := range filters {
		q.Set("f_"+dim, v)
	}
	if x != "" {
		q.Set("x", x)
	}
	if y != "" {
		q.Set("y", y)
	}
	if lang != i18n.Default {
		q.Set("lang", lang)
	}
	return q
}

// cubeAnchor keeps the grid in view across the reload a filter or axis
// change causes: without it every change scrolls the reader back to the
// top of the page and they lose their place in the drill-down.
const cubeAnchor = "#cube"

func cubeHref(base string, q url.Values) string {
	if enc := q.Encode(); enc != "" {
		return base + "?" + enc + cubeAnchor
	}
	return base + cubeAnchor
}

// validCubeAxis reports whether the query named a real dimension.
func validCubeAxis(key string) bool {
	for _, dim := range cubeDimKeys {
		if dim == key {
			return true
		}
	}
	return false
}

// buildCubeView assembles the explorer for one request. nil means the
// package has no cube-worthy evidence at all and the section is omitted.
func buildCubeView(s *site, r *http.Request, lang, eco, name string) *cubeView {
	facts, windowed := s.cubeFacts(r.Context(), eco, name)
	if len(facts) == 0 {
		return nil
	}
	pagePath := pkgHref(eco, name)
	q := r.URL.Query()
	filters := parseCubeFilters(q)
	sliced := filterCubeFacts(facts, filters)

	view := &cubeView{
		Action:     pagePath + cubeAnchor,
		HasFilters: len(filters) > 0,
		WindowNote: windowed,
	}
	if lang != i18n.Default {
		view.Lang = lang
	}
	view.ClearHref = cubeHref(pagePath, cubeQuery(nil, "", "", lang))

	// Filter chips, in stable dimension order. Removing a chip drops the
	// pin and lets the next view pick fresh default axes.
	for _, dim := range cubeDimKeys {
		v, ok := filters[dim]
		if !ok {
			continue
		}
		rest := map[string]string{}
		for d, val := range filters {
			if d != dim {
				rest[d] = val
			}
		}
		view.Chips = append(view.Chips, cubeChip{
			Dim:        dim,
			DimLabel:   i18n.T(lang, "cube.dim_"+dim),
			Value:      v,
			RemoveHref: cubeHref(pagePath, cubeQuery(rest, "", "", lang)),
		})
	}

	// Filter dropdowns for the dimensions NOT currently on an axis.
	//
	// A dimension spread across an axis is already fully visible: every one
	// of its values has its own row or column, so offering it again as a
	// dropdown asks the reader to pin the thing they are looking at. Picking
	// a value there collapses the axis to one line, which reads as the grid
	// breaking rather than as a filter working.
	//
	// Each remaining dropdown's options come from the slice narrowed by the
	// OTHER pins, so switching one filter can never offer a combination the
	// network has no evidence for.

	if len(sliced) == 0 {
		view.NoMatch = true
		return view
	}

	// What the reader has actually narrowed to. Everything below the cube on
	// the package page is a fact about ONE coordinate — that release's
	// dependencies, that environment's failures — and until the coordinate is
	// decided there is no honest way to show them. The page used to show them
	// anyway, all versions and all environments at once, which is why it read
	// as a pile rather than an answer.
	view.Coord = cubeCoord(sliced, filters)
	view.Decided = cubeCoordDecided(sliced)

	x, y := q.Get("x"), q.Get("y")
	if !validCubeAxis(x) || !validCubeAxis(y) || x == y {
		x, y = "", ""
	}
	// A dimension narrowed to a single value carries no spread; re-pick
	// the axes over what still varies. A whole-platform OS pin does still
	// vary, so it keeps its axis.
	if x != "" && (len(cubeDimValues(sliced, x)) < 2 && len(cubeDimValues(sliced, y)) < 2) {
		x, y = "", ""
	}
	// Explicitly chosen axes the slice never recorded would render an
	// empty shell; fall back to what actually varies.
	if x != "" && (len(cubeDimValues(sliced, x)) == 0 || len(cubeDimValues(sliced, y)) == 0) {
		x, y = "", ""
	}
	if x == "" {
		var ok bool
		x, y, ok = defaultCubeAxes(sliced, filters)
		if !ok {
			// The bottom of the drill-down. It still needs its controls: this
			// is where a reader arrives having pinned four things, and without
			// them there is no way back out of the coordinate they reached.
			view.Filters = cubeFilterBar(facts, "", "", filters, lang)
			view.Leaf = cubeLeafRows(sliced, eco, name)
			return view
		}
	}
	view.X, view.Y = x, y
	// A dimension spread across an axis is already fully visible: every one
	// of its values has its own row or column. Offering it again as a
	// dropdown asks the reader to pin the thing they are looking at, and
	// picking a value there collapses the axis to a single line -- which
	// reads as the grid breaking rather than as a filter working.
	//
	// The dropdowns are built before the axes are known, so they are dropped
	// here rather than skipped there.
	// Built here, not earlier, and from the evidence THIS grid renders. A
	// dimension on an axis needs no dropdown — every value already has its
	// own row or column, and picking one there collapses the axis to a single
	// line, which reads as the grid breaking rather than as a filter working.
	view.Filters = cubeFilterBar(facts, x, y, filters, lang)
	view.XLabel, view.YLabel = i18n.T(lang, "cube.dim_"+x), i18n.T(lang, "cube.dim_"+y)
	view.SwapHref = cubeHref(pagePath, cubeQuery(filters, y, x, lang))

	// Axis selectors offer every dimension the slice still spreads over.
	for _, dim := range cubeDimKeys {
		if len(cubeDimValues(sliced, dim)) < 2 && dim != x && dim != y {
			continue
		}
		label := i18n.T(lang, "cube.dim_"+dim)
		view.XOptions = append(view.XOptions, cubeAxisOption{Key: dim, Label: label, Selected: dim == x})
		view.YOptions = append(view.YOptions, cubeAxisOption{Key: dim, Label: label, Selected: dim == y})
	}

	// A cell pins both coordinates; a header pins just its own, which is
	// how a reader asks "everything on node 22" rather than one square of
	// it. Neither carries explicit axes: the next view re-defaults over
	// whatever still varies.
	pin := func(extra map[string]string) string {
		next := map[string]string{}
		for d, v := range filters {
			next[d] = v
		}
		for d, v := range extra {
			next[d] = v
		}
		return cubeHref(pagePath, cubeQuery(next, "", "", lang))
	}
	view.Grid = buildCubeGrid(sliced, x, y, pivotLinks{
		Cell: func(row, col string) string { return pin(map[string]string{x: col, y: row}) },
		Row:  func(row string) string { return pin(map[string]string{y: row}) },
		Col:  func(col string) string { return pin(map[string]string{x: col}) },
	}, time.Now(), true)
	return view
}

// cubeLeafRows renders the exact measured combinations of a bottomed-out
// slice, newest version first. Facts whose visible coordinates coincide
// (they differed only in a bucketed detail such as a runtime patch level)
// merge into one row instead of repeating it.
func cubeLeafRows(facts []cubeFact, eco, name string) []cubeLeafRow {
	now := time.Now()
	type leafKey struct{ version, symbol, env string }
	merged := map[leafKey][]cubeFact{}
	var order []leafKey
	for _, f := range facts {
		var envParts []string
		for _, dim := range []string{"os", "libc", "arch", "runtime", "tool", "context"} {
			if v := f.Dims[dim]; v != "" {
				envParts = append(envParts, v)
			}
		}
		key := leafKey{f.Dims["version"], f.Dims["symbol"], joinDims(envParts)}
		if _, seen := merged[key]; !seen {
			order = append(order, key)
		}
		merged[key] = append(merged[key], f)
	}
	rows := make([]cubeLeafRow, 0, len(order))
	for _, key := range order {
		row := cubeLeafRow{
			Version: key.version,
			Symbol:  key.symbol,
			Env:     key.env,
			Cell:    buildPivotCell(mergeCubeFacts(merged[key]), now),
		}
		if row.Symbol != cubePackageLevel && row.Symbol != "" {
			row.SymbolHref = symbolHref(eco, name, row.Version, row.Symbol)
		}
		if row.Version != "" {
			row.VersionHref = versionHref(eco, name, row.Version)
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if c := domain.CompareVersions(rows[i].Version, rows[j].Version); c != 0 {
			return c > 0
		}
		return rows[i].Symbol < rows[j].Symbol
	})
	return rows
}

func joinDims(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " · "
		}
		out += p
	}
	return out
}
