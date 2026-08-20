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

	// pivotStaleAfter is retained only by callers outside the cell; cells
	// no longer age, and neither does evidence anywhere else now. It once
	// mirrored a 90-day half-life
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
	Tone string
	// Glyph marks the rare thing: a cell WE RAN carries it, doubled when two
	// independent peers reproduced it. An unproven cell carries nothing here
	// and needs none -- badging the exception is the
	// point, since a mark on 95% of cells says nothing.
	//
	// It is deliberately not a check mark. A check means "passed" everywhere
	// it appears, and this marks the BASIS, so a cell we ran and that failed
	// rendered as a red tick beside 0/1 -- putting the verdict back into the
	// glyph after taking it out of the word.
	Glyph string // "◆" | "◆◆" | "" | "—"
	// Bang is retained as a field for the tooltip only; no marker renders.
	// A cell carries at most one symbol now -- the check -- and colour says
	// how the runs went. Every extra glyph was one more thing to learn
	// before the grid could be read at all.
	Bang bool
	// Cross is set when ≥2 distinct peers verified inside this bucket.
	Cross bool
	Href  string
	Tip   string // title attribute; English data values, never translated
	// Ratio is the observed pass rate a reader scans down a column ("95%"),
	// and Passes is the count it rests on ("297"). Both render: a percentage
	// alone discards how much evidence there is, and 100% of one run is not
	// 100% of a hundred.
	Ratio  string
	Passes string
	Runs   int64
	Obs    int64 // observation events (USED / PROJECT_*)
	Ver    int64 // verification events (RESOLVE…CONTRACT, SYMBOL_*)
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
	Cols    []pivotAxis
	Rows    []pivotGridRow
	HasBang bool
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
	// obsAttributed is the subset of obsFail whose sanitizer named a cause.
	// Observation evidence is co-occurrence, so an unattributed failure says
	// a build CONTAINING this package broke — one tsc failure wrote a FAIL
	// for all 412 packages in a lockfile, and 82% of production's failures
	// are that shape. They stay in the rate and the tooltip says how many
	// could be named, because otherwise a package that breaks and a package
	// that was merely installed read identically.
	obsAttributed int64
	// obsPeers is how many distinct peer buckets reported, as a PEAK and
	// never a sum: the same machine across two epochs is one machine. It is
	// what the cell weighs its rate by, because the event count answers a
	// different question — pgx v5.10.0 on go 1.26 carried 1,361 build
	// observations from a single bucket, and printing 1,154 beside the rate
	// read as 1,154 machines agreeing.
	obsPeers int64
	// used counts USED records: "this package was in the project". It is a
	// presence marker with no failing form — across the whole corpus it
	// carried 8,697 passes and zero failures, structurally, because there is
	// nothing for it to fail at — so it is kept out of the pass rate, where
	// it was 12.5% of every pass the network had recorded and could only
	// ever push a rate upward.
	used             int64
	verPass, verFail int64
	elevated         bool
	cross            bool
	obsLastSeen      string // max RFC3339 (UTC strings compare lexically)
	verLastSeen      string
	conf             int // best confidence seen: 3 HIGH, 2 MEDIUM, 1 LOW
}

// plural renders a bucket count, and says so when the producer recorded
// none: a zero here means the snapshot predates the field, not that nobody
// reported.
func plural(n int64) string {
	if n <= 0 {
		return "an unrecorded number of"
	}
	return fmt.Sprintf("%d", n)
}

func suffix(n int64) string {
	if n == 1 {
		return ""
	}
	return "s"
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
	return isUsageStageName(stage) || strings.HasPrefix(stage, "PROJECT_")
}

// isUsageStageName names the stage that records presence rather than an
// outcome. It is an observation — the package really was there, in a real
// project — but it is not a run, and a rate built from it measures how many
// people had the dependency installed.
func isUsageStageName(stage string) bool {
	return stage == string(domain.StageUsed)
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

// runtimeLineOf buckets a runtime version at the precision where that runtime
// actually breaks compatibility.
//
// The major alone is right for node and bun, where 20 and 22 are different
// lines. It is useless for Go and Python, whose major has not moved in a
// decade: every Go version buckets to "1", so a whole axis collapses into one
// meaningless column -- and beside a versionless "go" row it reads as the same
// thing listed twice.
func runtimeLineOf(runtime, version string) string {
	if version == "" {
		return ""
	}
	major := majorOf(version)
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "go", "golang", "python", "python3", "ruby", "php", "elixir", "erlang":
		return minorOf(version)
	}
	// Backstop for runtimes not named above: a major of 0 or 1 has almost
	// certainly not moved in years, so it buckets everything into one column.
	// This corrects itself when such a runtime finally ships a 2.
	if major == "0" || major == "1" {
		return minorOf(version)
	}
	return major
}

// minorOf keeps two segments: "1.25.3" becomes "1.25".
func minorOf(v string) string {
	i := strings.IndexByte(v, '.')
	if i < 0 {
		return v
	}
	if j := strings.IndexByte(v[i+1:], '.'); j >= 0 {
		return v[:i+1+j]
	}
	return v
}

// runtimeBucket names a runtime for an axis, saying so when the version was
// never recorded rather than rendering a bare name beside versioned ones,
// where it reads as a duplicate of the runtime rather than as a gap.
func runtimeBucket(runtime, version string) string {
	if runtime == "" {
		return ""
	}
	line := runtimeLineOf(runtime, version)
	if line == "" {
		return runtime + unrecordedAxisSuffix
	}
	return runtime + " " + line
}

// unrecordedAxisSuffix marks an axis value whose dimension was never
// captured. It is a constant so the front page can recognise the label where
// it is MADE rather than hunting for English in a page that ships in nine
// languages.
const unrecordedAxisSuffix = " (version not recorded)"

// isUnrecordedAxisLabel reports whether an axis value stands for a dimension
// nothing recorded.
func isUnrecordedAxisLabel(label string) bool {
	return strings.HasSuffix(label, unrecordedAxisSuffix)
}

// dropUnrecordedAxes removes the rows and columns that stand for a gap.
//
// The full explorer keeps them: there completeness is the point, and a reader
// who drilled in wants to know the dimension was never captured. The front
// page is the showcase, and a row reading "node (version not recorded)" is
// the worst thing to put in a shop window — it is a true statement about our
// instrument, not an answer to "does it run there".
func dropUnrecordedAxes(g pivotGrid) pivotGrid {
	keep := make([]int, 0, len(g.Cols))
	cols := make([]pivotAxis, 0, len(g.Cols))
	for i, c := range g.Cols {
		if !isUnrecordedAxisLabel(c.Label) {
			keep = append(keep, i)
			cols = append(cols, c)
		}
	}
	out := g
	out.Cols = cols
	out.Rows = nil
	for _, r := range g.Rows {
		if isUnrecordedAxisLabel(r.Label) {
			continue
		}
		trimmed := r
		trimmed.Cells = make([]pivotCell, 0, len(keep))
		for _, i := range keep {
			if i < len(r.Cells) {
				trimmed.Cells = append(trimmed.Cells, r.Cells[i])
			}
		}
		out.Rows = append(out.Rows, trimmed)
	}
	return out
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
				return runtimeBucket(e.Runtime, e.RuntimeVersion)
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
		if isUsageStageName(stage) {
			a.used += c.Pass + c.Fail
			hasObs = hasObs || c.Pass+c.Fail > 0
			continue
		}
		if isObservationStageName(stage) {
			a.obsPass += c.Pass
			a.obsFail += c.Fail
			a.obsAttributed += c.FailAttributed
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
	if int64(r.UniquePeerBuckets) > a.obsPeers {
		a.obsPeers = int64(r.UniquePeerBuckets)
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
	a.obsAttributed += b.obsAttributed
	a.used += b.used
	if b.obsPeers > a.obsPeers {
		a.obsPeers = b.obsPeers
	}
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

// events is whether this aggregate holds anything at all. Usage counts: a
// package seen in real projects is not an empty cell, even when nothing has
// been run against it.
func (a pivotAgg) events() int64 {
	return a.obsPass + a.obsFail + a.used + a.verPass + a.verFail
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

	// A grid with one row and one column has nothing left to narrow. Pinning
	// its row, its column or its only cell all produce the slice already on
	// screen, so every link on it invites the reader to arrive where they
	// started. The symbol page already refuses to draw a 1x1 summary above
	// the detail it repeats; this is the same fact one layer down.
	if len(rowsOrdered) == 1 && len(cols) == 1 {
		links = pivotLinks{}
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
	if obs == 0 && ver == 0 && a.used == 0 {
		return pivotCell{Class: "empty", Glyph: "—"}
	}
	// The number is usage; the mark is our own code. They answer different
	// questions, so a cell can hold either alone: a sample we verified that
	// nobody has been seen using keeps its mark and reports "—" where the
	// count would go. Blanking it entirely would erase the verified corpus
	// from the grid, because in production every verified cell has zero
	// observations -- the fleet proves on Linux and the world reports from
	// Windows.
	cell.Basis, cell.Class = "observed", "observed"
	cell.PassCount, cell.FailCount = a.obsPass, a.obsFail
	if ver > 0 {
		cell.Basis, cell.Class = "verified", "verified"
	}
	if obs == 0 {
		cell.PassCount, cell.FailCount = a.verPass, a.verFail
	}
	// The mark is our own run and how it went: a check when our sample passed
	// here, a cross when it failed, nothing when we have no sample for this
	// cell at all. One clean run is as much of that fact as a hundred.
	//
	// The cross exists because the alternative was colour alone. A verified
	// FAILURE with no observations rendered as a red "—", which reads as
	// "missing data, and somehow bad" -- the information was real and only
	// the hue carried it. Marks are also legible without colour.
	//
	// Cross reproduction gets no third symbol: it is a strictly better
	// version of the same fact and stays in the tooltip.
	switch {
	case a.verPass > 0 && a.verFail == 0:
		cell.Glyph = "✓"
	case a.verFail > 0:
		cell.Glyph = "✕"
	}
	switch {
	case obs == 0 && ver == 0:
		// Usage only: the package was there, and nothing ran. "No failures"
		// is satisfied by zero runs, so the pass colour was being granted to
		// cells that had no outcome to colour.
		cell.Tone = ""
	case cell.FailCount == 0:
		cell.Tone = "pass"
	case cell.PassCount == 0:
		cell.Tone = "fail"
	default:
		cell.Tone = "mixed"
	}
	cell.Bang = a.elevated || a.verFail > 0
	// Last-seen is still reported in the tooltip, but nothing is marked
	// stale any more. A cell is one pinned release in one environment
	// bucket, and neither moves: evidence that axios 1.6.0 failed on
	// node 20 does not become less true in ninety days. What can change is
	// the environment, and a new environment is a different cell.
	basisLastSeen := a.verLastSeen
	if ver == 0 {
		basisLastSeen = a.obsLastSeen
	}
	// Trouble is carried by COLOUR alone now. A marker for it was one more
	// symbol to hold, and Tone already reddens a cell whose runs mostly
	// failed -- the same fact without the extra glyph to learn.
	_ = a.elevated
	// Always rendered, including a single run. Suppressing the one-event
	// case hid exactly how thin a cell was behind a mark that looked the
	// same as a hundred agreeing runs.
	// The number is how many real machines got through it. Observations are
	// the asset here -- they reach platforms no container can -- and our own
	// runs are the mark, not the count.
	// Ratio scans, Passes weighs. A percentage can be compared down a column
	// at a glance and a raw count cannot; a percentage alone throws away how
	// much evidence it rests on, and 100% of one is not 100% of a hundred.
	// Both come from observations: our own runs are the mark, not the count.
	cell.Runs = obs
	switch {
	case obs > 0:
		cell.Ratio = fmt.Sprintf("%d%%", int(float64(a.obsPass)/float64(obs)*100+0.5))
		// The DENOMINATOR, which is what a rate rests on. It printed the
		// numerator once (1,154 beside 85%) and the peer-bucket count once
		// (1 beside 85%, which reads as 85% of one); neither is the number
		// the percentage divides by. How many machines reported is a real
		// and separate fact, and the tooltip carries it.
		cell.Passes = fmt.Sprintf("%d", obs)
	default:
		// Verified, never seen used. The dash sits where the rate would, so
		// the cell says "no usage recorded" rather than implying that zero
		// machines got through.
		cell.Ratio = "—"
	}

	var parts []string
	if obs > 0 {
		// "observed" alone was read as a count of people, and "build
		// observations" as a count of builds. It is neither: one build files
		// an observation per stage it reached — compile, test, typecheck —
		// so the events outnumber the builds several times over and both
		// readings overstate what happened.
		parts = append(parts, fmt.Sprintf("%d observations", obs))
	}
	if a.obsFail > 0 && a.obsAttributed < a.obsFail {
		parts = append(parts, fmt.Sprintf("%d of %d failures with an identified cause",
			a.obsAttributed, a.obsFail))
	}
	if a.used > 0 {
		// Stated separately because it answers a different question. These
		// are projects that HAD the package, not runs that exercised it.
		parts = append(parts, fmt.Sprintf("%d usage records", a.used))
	}
	if obs > 0 || a.used > 0 {
		// "machines" claimed more than a peer bucket is. A peer id is a hash
		// of a self-generated key with no registration behind it, so this
		// counts distinct KEYS that reported — one operator can hold as many
		// as they run workers, and a worker's key is stable per worker rather
		// than per machine. It still means something real: the same coordinate
		// was reported from more than one place. It is not a head count.
		parts = append(parts, fmt.Sprintf("%s reporting peer%s",
			plural(a.obsPeers), suffix(a.obsPeers)))
	}
	if ver > 0 {
		parts = append(parts, fmt.Sprintf("%d verified", ver))
	}
	// Both rates, each named. A cell with usage AND our own runs showed 85%
	// on its face and "pass 100%" in its tooltip, and nothing said the first
	// was the world's builds and the second was our sandbox — so the tooltip
	// read as a contradiction of the number it was explaining.
	if obs > 0 {
		parts = append(parts, fmt.Sprintf("observed pass %d%%",
			int(float64(a.obsPass)/float64(obs)*100+0.5)))
	}
	if ver > 0 {
		parts = append(parts, fmt.Sprintf("verified pass %d%%",
			int(float64(a.verPass)/float64(ver)*100+0.5)))
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
	cell.Tip = strings.Join(parts, " · ")
	return cell
}
