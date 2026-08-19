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
	Env        string // remaining recorded dimensions, " · " joined
	Cell       pivotCell
}

type cubeView struct {
	X, Y string
	// XLabel/YLabel name the spread axes in the page language, so the grid
	// says what it is a map of without reading the pickers.
	XLabel, YLabel string
	XOptions       []cubeAxisOption
	YOptions       []cubeAxisOption
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

	// Filter dropdowns for every dimension the cube recorded — usable
	// whether or not that dimension is currently spread across an axis.
	// Each dropdown's options come from the slice narrowed by the OTHER
	// pins, so switching one filter can never offer a combination the
	// network has no evidence for.
	for _, dim := range cubeDimKeys {
		rest := map[string]string{}
		for d, v := range filters {
			if d != dim {
				rest[d] = v
			}
		}
		values := cubeDimValues(filterCubeFacts(facts, rest), dim)
		if len(values) == 0 {
			continue
		}
		sel := cubeFilterSelect{
			Dim:    dim,
			Label:  i18n.T(lang, "cube.dim_"+dim),
			Active: filters[dim] != "",
		}
		sel.Options = append(sel.Options, cubeFilterOption{
			Label: i18n.T(lang, "cube.all"), Selected: filters[dim] == "",
		})
		for _, v := range values {
			sel.Options = append(sel.Options, cubeFilterOption{
				Value: v, Label: v, Selected: filters[dim] == v,
			})
		}
		view.Filters = append(view.Filters, sel)
	}

	if len(sliced) == 0 {
		view.NoMatch = true
		return view
	}

	x, y := q.Get("x"), q.Get("y")
	if !validCubeAxis(x) || !validCubeAxis(y) || x == y {
		x, y = "", ""
	}
	// A dimension pinned to one value carries no spread; if a filter took
	// over an axis, re-pick the axes over what still varies.
	if _, pinned := filters[x]; pinned {
		x, y = "", ""
	}
	if _, pinned := filters[y]; pinned {
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
			view.Leaf = cubeLeafRows(sliced, eco, name)
			return view
		}
	}
	view.X, view.Y = x, y
	view.XLabel, view.YLabel = i18n.T(lang, "cube.dim_"+x), i18n.T(lang, "cube.dim_"+y)

	// Axis selectors offer every unpinned dimension with recorded values.
	for _, dim := range cubeDimKeys {
		if _, pinned := filters[dim]; pinned {
			continue
		}
		if len(cubeDimValues(sliced, dim)) == 0 {
			continue
		}
		label := i18n.T(lang, "cube.dim_"+dim)
		view.XOptions = append(view.XOptions, cubeAxisOption{Key: dim, Label: label, Selected: dim == x})
		view.YOptions = append(view.YOptions, cubeAxisOption{Key: dim, Label: label, Selected: dim == y})
	}

	href := func(row, col string) string {
		next := map[string]string{}
		for d, v := range filters {
			next[d] = v
		}
		next[x] = col
		next[y] = row
		// No explicit axes: the next view re-defaults over what still varies.
		return cubeHref(pagePath, cubeQuery(next, "", "", lang))
	}
	view.Grid = buildCubeGrid(sliced, x, y, href, time.Now())
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
