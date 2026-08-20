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

// pivotCell is one (row, column) measurement of a pivoted compatibility grid.
//
// It states a RATE and names its BASIS, and it never speaks a verdict. "PASS"
// read as the general claim "this works here" when what was measured is
// "3 runs, 3 passed" -- and this project stands behind only the code it ran
// and the findings it detected, never behind a judgement it inferred from
// counts.
//
// The glyph carries the basis rather than the outcome for a specific reason:
// observation counts dwarf verification counts, so two bare ratios would make
// an anonymous cell look more authoritative than a proven one. Basis is the
// distinction that must never blur; pass-versus-fail is carried by the number.
type pivotCell struct {
	// Basis is who ran it: "verified" (the fleet ran a contract) or
	// "observed" (real machines reported a build), "" when nothing was
	// recorded. The vocabulary is the one the basis filter already emits
	// (filters.go), so the grid and the filter speak the same two words.
	// Basis is who ran it: "verified" or "observed", "" when nothing was
	// recorded. It is NOT rendered as text in the cell -- verification is the
	// scarce case (hundreds of packages against thousands observed), so
	// labelling the common one was noise on nearly every cell. It survives
	// for the class, the accessible label and the filter vocabulary.
	Basis string
	Class string // "verified" | "observed" | "empty"
	// Tone colours the cell by how the rate came out: "pass" | "fail" |
	// "mixed" | "". It is a visual affordance only and is never rendered as
	// text -- failure has to catch the eye, but the WORD claimed more than
	// the measurement supported, so only the colour survives.
	Tone  string
	// Glyph marks the rare thing. A cell we proved carries the verification
	// mark; an unproven one carries nothing here and is already marked weak
	// by Maybe. Badging the exception is the point: a mark on 95% of cells
	// says nothing.
	Glyph string // "✓" | "" | "—"
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
	// Ratio is the measurement itself: passes over runs, on the basis the
	// cell names. It is present whenever anything was recorded -- a lone
	// "1/1" says how thin the evidence is, which a bare mark concealed.
	Ratio string
	Obs   int64 // observation events (USED / PROJECT_*)
	Ver   int64 // verification events (RESOLVE…CONTRACT, SYMBOL_*)
	// PassCount and FailCount are the numerator and the remainder of Ratio,
	// on whichever basis the cell names.
	PassCount int64
	FailCount int64
}

// pivotAxis is one label along an axis, with the OS family it names when
// that axis is the operating system — the icon carries the family so the
// text beside it can spend its width on what actually differs
// ("alpine musl", "11") instead of repeating "linux" on every row.
type pivotAxis struct {
	Label string
	Icon  string // "linux" | "windows" | "macos" | ""
	// Href narrows the slice to this one value. A cell answers "this row
	// AND this column"; a header answers "this column, whatever the rows
	// say", which is the other half of reading a grid.
	Href string
}

type pivotGridRow struct {
	Label string
	Icon  string
	Href  string
	Cells []pivotCell // aligned with pivotGrid.Cols by index
}

// pivotLinks are the three ways into a grid: one cell, one whole row, one
// whole column. Any of them may be nil, which renders that part as text.
type pivotLinks struct {
	Cell func(row, col string) string
	Row  func(row string) string
	Col  func(col string) string
}

// osIcon names the icon family for an OS label, and the text that should
// sit beside it. "alpine musl" keeps all of its words; "windows 11" drops
// the family the icon already shows; a bare "linux" or "macos" has
// nothing left to say and shows the icon alone.
func osIcon(label string) (icon, text string) {
	fields := strings.Fields(label)
	if len(fields) == 0 {
		return "", label
	}
	// The family word is dropped only when something more specific
	// follows it — "windows 11" becomes the icon plus "11" — never when
	// dropping it would leave the row with no text at all.
	drop := func(family string) (string, string) {
		if rest := strings.Join(fields[1:], " "); rest != "" {
			return family, rest
		}
		return family, label
	}
	switch fields[0] {
	case "windows", "windowsservercore":
		return drop("windows")
	case "macos", "darwin":
		return drop("macos")
	case "linux":
		// "linux musl" says nothing without its first word: musl alone is
		// not an operating system.
		return "linux", label
	}
	// Everything else on the OS axis is a Linux distribution, and its
	// name is already the informative part.
	return "linux", label
}

// pivotGrid is a rendered rows × columns compatibility grid.
type pivotGrid struct {
	Cols     []pivotAxis
	Rows     []pivotGridRow
	HasBang  bool
	HasMaybe bool
	// Trimmed is set when the caps dropped lower-evidence rows or columns,
	// so the template can say the grid shows the most-measured slice.
	Trimmed bool
	// Scan-report strip: how many cells hold each verdict, how many hold
	// any measurement at all, and the newest evidence date in the grid.
	// All data values — the strip renders without translations.
	CountPass, CountFail, CountMixed, CountObserved int
	Measured                                        int
	LastSeen                                        string // date part
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

// osLabel names the operating system as precisely as the evidence
// recorded it.
//
// "linux" alone is not an answer to "does it run there": alpine/musl and
// debian/glibc are the difference between a native module loading and
// not. The producer files the distribution under osVersionBucket (and
// sometimes distro), so the label leads with that when it exists and
// keeps libc beside it. "" means no OS was recorded — never guessed.
func osLabel(e domain.EnvironmentFingerprint) string {
	os := strings.ToLower(strings.TrimSpace(e.OS))
	if os == "" {
		return ""
	}
	if os == "darwin" {
		os = "macos"
	}
	name := os
	if d := strings.ToLower(strings.TrimSpace(e.Distro)); d != "" {
		name = d
	} else if b := strings.ToLower(strings.TrimSpace(e.OSVersionBucket)); b != "" {
		// A bucket that is a release number stays attached to the OS
		// ("windows 11"); one that names a distribution replaces it
		// ("alpine"), because nobody needs telling that alpine is linux.
		if b[0] >= '0' && b[0] <= '9' {
			name = os + " " + b
		} else {
			name = b
		}
	}
	if e.Libc != "" {
		name += " " + e.Libc
	}
	return name
}

// osRowKey buckets a snapshot row by its recorded operating system.
func osRowKey(r snapshotRow) string {
	env := pivotEnv(r)
	if env == nil {
		return ""
	}
	return osLabel(env.Bucketed())
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
	// This pivot's rows are always the operating system.
	return assembleGrid(aggs, sortPivotRows, sortPivotCols, true, false,
		pivotLinks{Cell: cellHref}, now)
}

// assembleGrid turns aggregated cells into an ordered, capped, rendered
// grid. Evidence totals rank what the caps keep; display never sums the
// two classes.
func assembleGrid(aggs map[cellKey]*pivotAgg,
	sortRowsFn, sortColsFn func([]string) []string,
	rowsAreOS, colsAreOS bool,
	links pivotLinks, now time.Time) pivotGrid {

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

	g := pivotGrid{Trimmed: trimmed}
	for _, c := range cols {
		axis := pivotAxis{Label: c}
		if colsAreOS {
			axis.Icon, axis.Label = osIcon(c)
		}
		if links.Col != nil {
			axis.Href = links.Col(c)
		}
		g.Cols = append(g.Cols, axis)
	}
	lastSeen := ""
	for _, rk := range rowsOrdered {
		row := pivotGridRow{Label: rk, Cells: make([]pivotCell, 0, len(cols))}
		if rowsAreOS {
			row.Icon, row.Label = osIcon(rk)
		}
		if links.Row != nil {
			row.Href = links.Row(rk)
		}
		for _, ck := range cols {
			a := aggs[cellKey{rk, ck}]
			cell := buildPivotCell(a, now)
			if cell.Class != "empty" && links.Cell != nil {
				cell.Href = links.Cell(rk, ck)
			}
			if cell.Bang {
				g.HasBang = true
			}
			if cell.Maybe {
				g.HasMaybe = true
			}
			switch {
			case cell.Basis == "observed":
				g.CountObserved++
			case cell.Basis != "" && cell.FailCount == 0:
				g.CountPass++
			case cell.Basis != "" && cell.PassCount == 0:
				g.CountFail++
			case cell.Basis != "":
				g.CountMixed++
			}
			if cell.Class != "empty" {
				g.Measured++
			}
			if a != nil {
				if a.obsLastSeen > lastSeen {
					lastSeen = a.obsLastSeen
				}
				if a.verLastSeen > lastSeen {
					lastSeen = a.verLastSeen
				}
			}
			row.Cells = append(row.Cells, cell)
		}
		g.Rows = append(g.Rows, row)
	}
	g.LastSeen = datePart(lastSeen)
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
	// Familiar order, now that a row may be named for its distribution
	// ("alpine musl") rather than its kernel: everything that is not
	// macOS or Windows is a Linux-family row and leads.
	rank := func(label string) int {
		fields := strings.Fields(label)
		if len(fields) == 0 {
			return 3
		}
		switch fields[0] {
		case "macos":
			return 1
		case "windows":
			return 2
		}
		return 0
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
	// A verification, however small, outranks any volume of observation:
	// the basis is about who ran it, never about how many said so.
	switch {
	case ver > 0:
		cell.Basis, cell.Class, cell.Glyph = "verified", "verified", "✓"
		cell.PassCount, cell.FailCount = a.verPass, a.verFail
	case obs > 0:
		cell.Basis, cell.Class, cell.Glyph = "observed", "observed", ""
		cell.PassCount, cell.FailCount = a.obsPass, a.obsFail
	default:
		return pivotCell{Class: "empty", Glyph: "—"}
	}
	switch {
	case cell.FailCount == 0:
		cell.Tone = "pass"
	case cell.PassCount == 0:
		cell.Tone = "fail"
	default:
		cell.Tone = "mixed"
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
	// Always rendered, including "1/1". Suppressing the single-event case
	// hid exactly how thin a cell was behind a mark that looked the same as
	// a hundred agreeing runs.
	cell.Ratio = fmt.Sprintf("%d/%d", cell.PassCount, cell.PassCount+cell.FailCount)

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
