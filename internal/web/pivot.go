package web

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ---------------------------------------------------------------------------
// Pivoted compatibility grids: the same snapshot rows every detail table
// renders, folded into an OS × runtime-line matrix a visitor reads in
// seconds. The pivot only regroups — observations and verifications stay
// separate inside every cell (goal.md §3.5), and a dimension the evidence
// never recorded is skipped, not guessed.

const (
	// pivotMaxCols/Rows bound the grid so it stays a summary. When the cap
	// trims anything the grid says so (Trimmed) — the detail tables below
	// each pivot carry the full row set.
	pivotMaxCols = 8
	pivotMaxRows = 6

	// pivotStaleAfter mirrors the 90-day half-life of RecencyDecay
	// (internal/compatibility/confidence.go): evidence that old still
	// renders, but carries the "?" marker and says "stale".
	pivotStaleAfter = 90 * 24 * time.Hour
)

// pivotCell is one (row, column) verdict of a pivoted compatibility grid.
type pivotCell struct {
	State string // "PASS" | "FAIL" | "MIXED" | "OBSERVED" | "" (no evidence)
	Class string // "pass" | "fail" | "mixed" | "observed" | "empty"
	Glyph string // "✓" | "✕" | "◐" | "○" | "—"
	// Bang marks a measured anomaly: an elevated failure rate or any
	// verification FAIL. Maybe marks weak or aged evidence: a cell proven
	// only by project observations, or one whose newest evidence is stale.
	Bang  bool
	Maybe bool
	Stale bool
	// Cross is set when ≥2 distinct peers verified inside this bucket.
	Cross bool
	Href  string
	Tip   string // title attribute; English data values, never translated
	// Ratio says how much variation a state summarizes: "15/18" verified
	// passes (or observed passes when nothing is verified). Empty for a
	// single-event cell — a bare PASS there is already the whole truth.
	Ratio string
	Obs   int64 // observation events (USED / PROJECT_*)
	Ver   int64 // verification events (RESOLVE…CONTRACT, SYMBOL_*)
}

type pivotGridRow struct {
	Label string
	Cells []pivotCell // aligned with pivotGrid.Cols by index
}

// pivotGrid is a rendered rows × columns compatibility grid.
type pivotGrid struct {
	Cols     []string
	Rows     []pivotGridRow
	HasBang  bool
	HasMaybe bool
	// Trimmed is set when the caps dropped lower-evidence rows or columns,
	// so the template can say the grid shows the most-measured slice.
	Trimmed bool
}

// Empty reports whether the grid has nothing worth rendering.
func (g pivotGrid) Empty() bool { return len(g.Rows) == 0 || len(g.Cols) == 0 }

// pivotAgg accumulates every snapshot row that lands in one cell.
//
// The two evidence classes keep separate recency: a fresh build
// observation must not refresh the "last seen" of a stale verification,
// because the cell's verdict comes from the verification side.
type pivotAgg struct {
	obsPass, obsFail int64
	verPass, verFail int64
	elevated         bool
	cross            bool
	obsLastSeen      string // max RFC3339 (UTC strings compare lexically)
	verLastSeen      string
	conf             int // best confidence seen: 3 HIGH, 2 MEDIUM, 1 LOW
}

func confRank(c string) int {
	switch strings.ToUpper(strings.TrimSpace(c)) {
	case "HIGH":
		return 3
	case "MEDIUM":
		return 2
	case "LOW":
		return 1
	}
	return 0
}

func confName(rank int) string {
	switch rank {
	case 3:
		return "HIGH"
	case 2:
		return "MEDIUM"
	case 1:
		return "LOW"
	}
	return ""
}

// isObservationStageName classifies a stage the way splitStageCounts does:
// USED and PROJECT_* are weak project observations, everything else is
// verification evidence.
func isObservationStageName(stage string) bool {
	return stage == string(domain.StageUsed) || strings.HasPrefix(stage, "PROJECT_")
}

// pivotEnv resolves the row's environment, honouring the legacy alias.
func pivotEnv(r snapshotRow) *domain.EnvironmentFingerprint {
	if r.Env != nil {
		return r.Env
	}
	return r.EnvAlias
}

// osRowKey buckets a snapshot row by OS plus libc when recorded — musl vs
// glibc decides whether a native module loads at all. "" means the
// evidence never recorded an OS and the row joins no pivot row.
func osRowKey(r snapshotRow) string {
	env := pivotEnv(r)
	if env == nil {
		return ""
	}
	e := env.Bucketed()
	os := strings.ToLower(strings.TrimSpace(e.OS))
	if os == "" {
		return ""
	}
	if os == "darwin" {
		os = "macos"
	}
	if e.Libc != "" {
		os += " " + e.Libc
	}
	return os
}

// majorOf reduces "22.18" to "22"; a bare major passes through.
func majorOf(v string) string {
	if i := strings.IndexByte(v, '.'); i > 0 {
		return v[:i]
	}
	return v
}

// contextColKey buckets by browser family or runtime line + MAJOR version
// ("node 22", "safari 19"), falling back to the materialized context label.
func contextColKey(r snapshotRow) string {
	if env := pivotEnv(r); env != nil {
		e := env.Bucketed()
		if e.BrowserFamily != "" {
			if e.BrowserMajor != "" {
				return e.BrowserFamily + " " + majorOf(e.BrowserMajor)
			}
			return e.BrowserFamily
		}
		if e.Runtime != "" {
			if e.RuntimeVersion != "" {
				return e.Runtime + " " + majorOf(e.RuntimeVersion)
			}
			return e.Runtime
		}
	}
	label := strings.TrimSpace(r.ContextLabel)
	if label == "" || label == "unknown" {
		return ""
	}
	if i := strings.LastIndexByte(label, ' '); i > 0 {
		return label[:i] + " " + majorOf(label[i+1:])
	}
	return label
}

// cellKey addresses one cell of a grid under assembly.
type cellKey struct{ row, col string }

// absorbRow folds one snapshot row's stage counts and quality signals
// into a. The row's LastSeen refreshes only the evidence classes the row
// actually carries.
func (a *pivotAgg) absorbRow(r snapshotRow) {
	var hasObs, hasVer bool
	for stage, c := range r.ByStage {
		if isObservationStageName(stage) {
			a.obsPass += c.Pass
			a.obsFail += c.Fail
			hasObs = hasObs || c.Pass+c.Fail > 0
		} else {
			a.verPass += c.Pass
			a.verFail += c.Fail
			hasVer = hasVer || c.Pass+c.Fail > 0
		}
	}
	if r.ElevatedFailure {
		a.elevated = true
	}
	if r.VerificationCounts["distinctVerifyingPeers"] >= 2 {
		a.cross = true
	}
	if hasObs && r.LastSeen > a.obsLastSeen {
		a.obsLastSeen = r.LastSeen
	}
	if hasVer && r.LastSeen > a.verLastSeen {
		a.verLastSeen = r.LastSeen
	}
	if c := confRank(r.Confidence); c > a.conf {
		a.conf = c
	}
}

// merge folds another aggregate into a, keeping the two evidence classes
// apart and every quality signal at its strongest observed value.
func (a *pivotAgg) merge(b pivotAgg) {
	a.obsPass += b.obsPass
	a.obsFail += b.obsFail
	a.verPass += b.verPass
	a.verFail += b.verFail
	a.elevated = a.elevated || b.elevated
	a.cross = a.cross || b.cross
	if b.obsLastSeen > a.obsLastSeen {
		a.obsLastSeen = b.obsLastSeen
	}
	if b.verLastSeen > a.verLastSeen {
		a.verLastSeen = b.verLastSeen
	}
	if b.conf > a.conf {
		a.conf = b.conf
	}
}

// mergeObservations folds only b's observation side (plus its quality
// flags) into a — the verification side is handled by the caller's
// dedup rules.
func (a *pivotAgg) mergeObservations(b pivotAgg) {
	a.obsPass += b.obsPass
	a.obsFail += b.obsFail
	a.elevated = a.elevated || b.elevated
	if b.obsLastSeen > a.obsLastSeen {
		a.obsLastSeen = b.obsLastSeen
	}
	if b.conf > a.conf {
		a.conf = b.conf
	}
}

// mergeVerifications folds only b's verification side into a.
func (a *pivotAgg) mergeVerifications(b pivotAgg) {
	a.verPass += b.verPass
	a.verFail += b.verFail
	a.cross = a.cross || b.cross
	if b.verLastSeen > a.verLastSeen {
		a.verLastSeen = b.verLastSeen
	}
	if b.conf > a.conf {
		a.conf = b.conf
	}
}

func (a pivotAgg) events() int64 {
	return a.obsPass + a.obsFail + a.verPass + a.verFail
}

// buildPivot folds snapshot rows into a rowKey × colKey grid. cellHref may
// be nil; it is asked only for cells that hold evidence.
func buildPivot(rows []snapshotRow, rowKey, colKey func(r snapshotRow) string,
	cellHref func(row, col string) string, now time.Time) pivotGrid {

	aggs := map[cellKey]*pivotAgg{}
	for _, r := range rows {
		rk, ck := rowKey(r), colKey(r)
		if rk == "" || ck == "" {
			continue
		}
		key := cellKey{rk, ck}
		a := aggs[key]
		if a == nil {
			a = &pivotAgg{}
			aggs[key] = a
		}
		a.absorbRow(r)
	}
	return assembleGrid(aggs, sortPivotRows, sortPivotCols, cellHref, now)
}

// assembleGrid turns aggregated cells into an ordered, capped, rendered
// grid. Evidence totals rank what the caps keep; display never sums the
// two classes.
func assembleGrid(aggs map[cellKey]*pivotAgg,
	sortRowsFn, sortColsFn func([]string) []string,
	cellHref func(row, col string) string, now time.Time) pivotGrid {

	if len(aggs) == 0 {
		return pivotGrid{}
	}
	rowEvidence := map[string]int64{}
	colEvidence := map[string]int64{}
	for key, a := range aggs {
		rowEvidence[key.row] += a.events()
		colEvidence[key.col] += a.events()
	}

	cols := sortColsFn(keysOf(colEvidence))
	rowsOrdered := sortRowsFn(keysOf(rowEvidence))

	var trimmed bool
	if len(cols) > pivotMaxCols {
		cols = sortColsFn(capByEvidence(cols, colEvidence, pivotMaxCols))
		trimmed = true
	}
	if len(rowsOrdered) > pivotMaxRows {
		rowsOrdered = sortRowsFn(capByEvidence(rowsOrdered, rowEvidence, pivotMaxRows))
		trimmed = true
	}

	g := pivotGrid{Cols: cols, Trimmed: trimmed}
	for _, rk := range rowsOrdered {
		row := pivotGridRow{Label: rk, Cells: make([]pivotCell, 0, len(cols))}
		for _, ck := range cols {
			a := aggs[cellKey{rk, ck}]
			cell := buildPivotCell(a, now)
			if cell.Class != "empty" && cellHref != nil {
				cell.Href = cellHref(rk, ck)
			}
			if cell.Bang {
				g.HasBang = true
			}
			if cell.Maybe {
				g.HasMaybe = true
			}
			row.Cells = append(row.Cells, cell)
		}
		g.Rows = append(g.Rows, row)
	}
	return g
}

func keysOf(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// capByEvidence keeps the n highest-evidence labels.
func capByEvidence(labels []string, evidence map[string]int64, n int) []string {
	sorted := append([]string(nil), labels...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if evidence[sorted[i]] != evidence[sorted[j]] {
			return evidence[sorted[i]] > evidence[sorted[j]]
		}
		return sorted[i] < sorted[j]
	})
	return sorted[:n]
}

// sortPivotCols orders columns line-alphabetical, newest major first
// within a line; versionless labels close their line.
func sortPivotCols(cols []string) []string {
	type colInfo struct {
		label, line string
		major       int
		hasMajor    bool
	}
	infos := make([]colInfo, 0, len(cols))
	for _, c := range cols {
		info := colInfo{label: c, line: c}
		if i := strings.LastIndexByte(c, ' '); i > 0 {
			if n, err := strconv.Atoi(c[i+1:]); err == nil {
				info.line, info.major, info.hasMajor = c[:i], n, true
			}
		}
		infos = append(infos, info)
	}
	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].line != infos[j].line {
			return infos[i].line < infos[j].line
		}
		if infos[i].hasMajor != infos[j].hasMajor {
			return infos[i].hasMajor
		}
		if infos[i].major != infos[j].major {
			return infos[i].major > infos[j].major
		}
		return infos[i].label < infos[j].label
	})
	out := make([]string, len(infos))
	for i, info := range infos {
		out[i] = info.label
	}
	return out
}

// sortPivotRows keeps the familiar linux, macos, windows order; other
// operating systems follow alphabetically, libc variants beside their base.
func sortPivotRows(rows []string) []string {
	rank := func(label string) int {
		switch strings.Fields(label)[0] {
		case "linux":
			return 0
		case "macos":
			return 1
		case "windows":
			return 2
		}
		return 3
	}
	sorted := append([]string(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if rank(sorted[i]) != rank(sorted[j]) {
			return rank(sorted[i]) < rank(sorted[j])
		}
		return sorted[i] < sorted[j]
	})
	return sorted
}

func buildPivotCell(a *pivotAgg, now time.Time) pivotCell {
	if a == nil {
		return pivotCell{Class: "empty", Glyph: "—"}
	}
	obs := a.obsPass + a.obsFail
	ver := a.verPass + a.verFail
	cell := pivotCell{Obs: obs, Ver: ver, Cross: a.cross}
	switch {
	case a.verPass > 0 && a.verFail == 0:
		cell.State, cell.Class, cell.Glyph = "PASS", "pass", "✓"
	case a.verFail > 0 && a.verPass == 0:
		cell.State, cell.Class, cell.Glyph = "FAIL", "fail", "✕"
	case a.verPass > 0 && a.verFail > 0:
		cell.State, cell.Class, cell.Glyph = "MIXED", "mixed", "◐"
	case obs > 0:
		cell.State, cell.Class, cell.Glyph = "OBSERVED", "observed", "○"
	default:
		return pivotCell{Class: "empty", Glyph: "—"}
	}
	// Staleness follows the evidence class the cell's verdict comes from:
	// a fresh observation never freshens a stale verification's PASS.
	basisLastSeen := a.verLastSeen
	if ver == 0 {
		basisLastSeen = a.obsLastSeen
	}
	if basisLastSeen != "" {
		if ts, err := time.Parse(time.RFC3339, basisLastSeen); err == nil && now.Sub(ts) > pivotStaleAfter {
			cell.Stale = true
		}
	}
	cell.Bang = a.elevated || a.verFail > 0
	cell.Maybe = ver == 0 || cell.Stale
	if ver > 1 {
		cell.Ratio = fmt.Sprintf("%d/%d", a.verPass, ver)
	} else if ver == 0 && obs > 1 {
		cell.Ratio = fmt.Sprintf("%d/%d", a.obsPass, obs)
	}

	var parts []string
	if obs > 0 {
		parts = append(parts, fmt.Sprintf("%d observed", obs))
	}
	if ver > 0 {
		parts = append(parts, fmt.Sprintf("%d verified", ver))
	}
	if ver > 0 {
		parts = append(parts, fmt.Sprintf("pass %d%%", int(float64(a.verPass)/float64(ver)*100+0.5)))
	} else if obs > 0 {
		parts = append(parts, fmt.Sprintf("pass %d%%", int(float64(a.obsPass)/float64(obs)*100+0.5)))
	}
	if c := confName(a.conf); c != "" {
		parts = append(parts, c)
	}
	if basisLastSeen != "" {
		parts = append(parts, "last seen "+datePart(basisLastSeen))
	}
	if cell.Bang {
		parts = append(parts, "anomaly")
	}
	if cell.Cross {
		parts = append(parts, "cross-checked")
	}
	if cell.Stale {
		parts = append(parts, "stale")
	}
	cell.Tip = strings.Join(parts, " · ")
	return cell
}
