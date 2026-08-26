package web

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ---------------------------------------------------------------------------
// The compatibility cube: every snapshot row of a package, tagged with the
// dimensions it was measured under. A 2D grid is only ever a slice of it —
// the explorer pins filters one cell at a time until a single measured
// combination remains, and that leaf is the exact contract record.
//
// Everything here reads materialized snapshots through the existing Store
// interface; the cube is assembled in the page layer, never in the backend.

const (
	// cubeMaxVersions and cubeMaxSymbolsPerVersion bound how many
	// snapshots one cube assembly reads. The newest versions and the
	// first-listed symbols carry the traffic; the per-page detail tables
	// remain the unabridged record.
	cubeMaxVersions          = 6
	cubeMaxSymbolsPerVersion = 10

	// cubePackageLevel labels evidence recorded against the package as a
	// whole (snapshot symbol ""). It is disjoint from per-symbol evidence,
	// not a roll-up of it.
	cubePackageLevel = "whole package"

	// cubeTTL is how long one package's assembled cube is reused.
	cubeTTL = 5 * time.Minute
	// cubeLoadTimeout bounds one assembly. It replaces the initiating
	// request's deadline, which is not the right clock for shared work.
	cubeLoadTimeout = 30 * time.Second
	// cubeCacheMax bounds the per-process cube cache.
	cubeCacheMax = 64
)

// cubeDimKeys is the dimension vocabulary in display order. Each key is
// also the ?f_<key>= query parameter that pins it.
var cubeDimKeys = []string{"os", "runtime", "version", "symbol", "arch", "tool", "context", "libc"}

// Axis defaults: version across, symbol down. That is the question the site
// exists to answer -- does this API work in this release -- and it is the
// pair that produces a grid rather than a diagonal.
//
// It used to be runtime across and OS down, which on this corpus guarantees
// emptiness: every observation is recorded on Windows and every verification
// runs on Linux, so an OS axis files the two halves into different rows and
// no cell can ever hold both. OS remains a filter, where a degenerate
// dimension belongs.
var (
	cubeXAxisPriority = []string{"version", "runtime", "symbol", "context", "tool", "arch", "libc", "os"}
	cubeYAxisPriority = []string{"symbol", "os", "arch", "libc", "tool", "runtime", "version", "context"}
)

// cubeFact is one snapshot row placed in the cube.
//
// EnvHash and PackageLevel exist for verification dedup: the producer
// files the SAME verification receipt into the package-level ("") snapshot
// and into every claimed symbol's snapshot, so summing verification counts
// across the symbol dimension would multiply one contract run. Observation
// evidence IS disjoint per symbol and sums normally.
type cubeFact struct {
	Dims map[string]string // dim key → value; "" = never recorded
	Agg  pivotAgg
	// EnvHash identifies the environment bucket the row was filed under;
	// "" when the row recorded no environment.
	EnvHash string
	// PackageLevel marks facts from the symbol-"" snapshot, which carries
	// the superset of every receipt in its environment bucket.
	PackageLevel bool
}

// cubeFactFromRow tags a snapshot row with its source coordinates. Rows
// with no stage counts at all carry no evidence and produce no fact.
func cubeFactFromRow(r snapshotRow, version, symbol string) (cubeFact, bool) {
	var agg pivotAgg
	agg.absorbRow(r)
	if agg.events() == 0 {
		return cubeFact{}, false
	}
	sym := symbol
	if sym == "" {
		sym = cubePackageLevel
	}
	fact := cubeFact{Agg: agg, PackageLevel: symbol == ""}
	dims := map[string]string{"version": version, "symbol": sym}
	if env := pivotEnv(r); env != nil {
		fact.EnvHash = env.Bucketed().Hash()
		e := env.Bucketed()
		dims["os"] = osLabel(e)
		dims["arch"] = e.Arch
		if e.Runtime != "" {
			dims["runtime"] = runtimeBucket(e.Runtime, e.RuntimeVersion)
		}
		if e.PackageManager != "" {
			tool := e.PackageManager
			if e.PackageManagerVersion != "" {
				tool += " " + majorOf(e.PackageManagerVersion)
			}
			dims["tool"] = tool
		}
		if e.BrowserFamily != "" {
			c := e.BrowserFamily
			if e.BrowserMajor != "" {
				c += " " + majorOf(e.BrowserMajor)
			}
			dims["context"] = c
		} else if e.ExecutionContext != "" {
			dims["context"] = e.ExecutionContext
		}
		dims["libc"] = e.Libc
	}
	fact.Dims = dims
	return fact, true
}

// loadCubeFacts assembles a package's cube from its materialized
// snapshots: the newest versions, each with its package-level snapshot and
// its first symbols. windowed reports whether that assembly window dropped
// versions or symbols — the page says so rather than letting an absent
// cell read as "never measured".
func loadCubeFacts(ctx context.Context, store Store, eco, name string) (facts []cubeFact, windowed bool, err error) {
	versions, err := store.PackageVersions(ctx, eco, name)
	if err != nil {
		return nil, false, err
	}
	if len(versions) > cubeMaxVersions {
		versions = versions[:cubeMaxVersions]
		windowed = true
	}
	for _, v := range versions {
		purl := domain.PURL{Ecosystem: eco, Name: name, Version: v}.String()
		symbols, err := store.PackageSymbols(ctx, eco, name, v)
		if err != nil {
			symbols = nil
		}
		if len(symbols) > cubeMaxSymbolsPerVersion {
			symbols = symbols[:cubeMaxSymbolsPerVersion]
			windowed = true
		}
		for _, sym := range append([]string{""}, symbols...) {
			raw, ok := store.SnapshotJSON(ctx, purl, sym)
			if !ok {
				continue
			}
			var doc snapshotDoc
			if json.Unmarshal([]byte(raw), &doc) != nil {
				continue
			}
			for _, row := range doc.Rows {
				if fact, ok := cubeFactFromRow(row, v, sym); ok {
					facts = append(facts, fact)
				}
			}
		}
	}
	return facts, windowed, nil
}

// loadPinnedCubeFacts repairs the browse window for a coordinate the reader
// named outright.
//
// The window — newest cubeMaxVersions releases, first cubeMaxSymbolsPerVersion
// symbols of each — is the right bound for BROWSING, where the reader is
// looking for the busy part of a package and an assembly has to end
// somewhere. It is the wrong bound for a URL that already says which release
// and which symbol it is about, because the window moves and the URL does
// not: every new release pushes an older one out, and on the day it leaves,
// every link anyone ever shared for that release starts answering "no
// recorded evidence matches these filters". The evidence never moved.
//
// So a pin is read directly. It is a repair and not a second assembly: when
// the window already covered the pin this returns nil without touching the
// store, and what it reads otherwise is the pinned coordinate alone.
func loadPinnedCubeFacts(ctx context.Context, store Store, eco, name string,
	have []cubeFact, filters map[string]string) []cubeFact {

	if !cubePinNeedsLoad(have, filters) {
		return nil
	}
	wantVersion, wantSymbol := filters["version"], filters["symbol"]

	versions := []string{wantVersion}
	if wantVersion == "" {
		// Only the symbol is pinned: it is looked for in the releases the
		// window already knows about, which bounds this at one snapshot read
		// per version rather than one per release the package has ever had.
		versions = cubeDimValues(have, "version")
		if len(versions) == 0 {
			all, err := store.PackageVersions(ctx, eco, name)
			if err != nil {
				return nil
			}
			if len(all) > cubeMaxVersions {
				all = all[:cubeMaxVersions]
			}
			versions = all
		}
	}

	var out []cubeFact
	for _, v := range versions {
		if v == "" {
			continue
		}
		purl := domain.PURL{Ecosystem: eco, Name: name, Version: v}.String()
		symbols := []string{snapshotSymbol(wantSymbol)}
		if wantSymbol == "" {
			// A pinned version with no symbol pin gets the same shape the
			// browse assembly would have given it, had it reached that far.
			syms, err := store.PackageSymbols(ctx, eco, name, v)
			if err != nil {
				syms = nil
			}
			if len(syms) > cubeMaxSymbolsPerVersion {
				syms = syms[:cubeMaxSymbolsPerVersion]
			}
			symbols = append([]string{""}, syms...)
		}
		for _, sym := range symbols {
			raw, ok := store.SnapshotJSON(ctx, purl, sym)
			if !ok {
				continue
			}
			var doc snapshotDoc
			if json.Unmarshal([]byte(raw), &doc) != nil {
				continue
			}
			for _, row := range doc.Rows {
				if fact, ok := cubeFactFromRow(row, v, sym); ok {
					out = append(out, fact)
				}
			}
		}
	}
	return out
}

// cubePinNeedsLoad reports whether the reader named a coordinate the browse
// window did not read.
//
// It tests the PAIR, not the two names separately. A symbol that is the first
// symbol of one release and the twelfth of another is present on both
// single-name probes and absent from the coordinate the reader asked for —
// which is the case a version check and a symbol check each wave through.
func cubePinNeedsLoad(have []cubeFact, filters map[string]string) bool {
	probe := map[string]string{}
	if v := filters["version"]; v != "" {
		probe["version"] = v
	}
	if v := filters["symbol"]; v != "" {
		probe["symbol"] = v
	}
	if len(probe) == 0 {
		return false
	}
	return len(filterCubeFacts(have, probe)) == 0
}

// pinnedCubeEntry is one cached repair read.
type pinnedCubeEntry struct {
	facts []cubeFact
	at    time.Time
}

// pinnedCubeFactsCached is loadPinnedCubeFacts with the store read memoized.
//
// The probe is deliberately re-run every time and never cached: whether the
// browse window covers this pin is a question about the CURRENT assembly, and
// answering it from a stale cache would append facts the assembly already
// holds. Only the read is cached, and only after the probe has said a read is
// needed — so a warm assembly that covers the pin still costs nothing.
func (s *site) pinnedCubeFactsCached(ctx context.Context, eco, name string,
	have []cubeFact, filters map[string]string) []cubeFact {

	if !cubePinNeedsLoad(have, filters) {
		return nil
	}
	key := eco + "|" + name + "|" + filters["version"] + "|" + filters["symbol"]
	now := time.Now()

	s.cubeMu.Lock()
	e, ok := s.pinnedCube[key]
	s.cubeMu.Unlock()
	if ok && now.Sub(e.at) < cubeTTL {
		return e.facts
	}

	facts := loadPinnedCubeFacts(ctx, s.d.Store, eco, name, have, filters)

	s.cubeMu.Lock()
	defer s.cubeMu.Unlock()
	if s.pinnedCube == nil {
		s.pinnedCube = map[string]pinnedCubeEntry{}
	}
	// Bounded the same way the cube cache is, and for the same reason: the
	// key includes a value from the query string, so an unbounded map is a
	// crawler away from being the process's memory.
	if len(s.pinnedCube) >= cubeCacheMax {
		for k, old := range s.pinnedCube {
			if now.Sub(old.at) >= cubeTTL {
				delete(s.pinnedCube, k)
			}
		}
		if len(s.pinnedCube) >= cubeCacheMax {
			for k := range s.pinnedCube {
				delete(s.pinnedCube, k)
				break
			}
		}
	}
	s.pinnedCube[key] = pinnedCubeEntry{facts: facts, at: now}
	return facts
}

// snapshotSymbol maps a cube dimension value back to the symbol a snapshot is
// filed under. Package-level evidence is stored as "" and displayed — and
// therefore pinned in URLs — as cubePackageLevel.
func snapshotSymbol(dimValue string) string {
	if dimValue == cubePackageLevel {
		return ""
	}
	return dimValue
}

// cubeWindowNote decides whether the assembly window can still explain an
// absence on THIS slice.
//
// The note exists so an empty cell does not read as "never measured": the
// window drops old versions and late symbols, and a browse grid cannot tell
// the reader which kind of empty it is looking at. Once both the version and
// the symbol are pinned, the coordinate is read directly by
// loadPinnedCubeFacts and the window has dropped nothing that bears on it —
// leaving the note there would ask the reader to doubt a record that was
// fetched on purpose, which is the same confusion pointed the other way.
//
// One pin is not enough. A pinned version still has its symbols capped, and
// a pinned symbol is still looked for inside a bounded version list.
func cubeWindowNote(windowed bool, filters map[string]string) bool {
	if !windowed {
		return false
	}
	return filters["version"] == "" || filters["symbol"] == ""
}

// osFamilies are the whole-platform values the OS filter accepts beside
// the exact ones. "linux" answers "does it run on Linux at all", which is
// a different question from "does it run on alpine musl" — and both are
// questions a reader arrives with.
var osFamilies = []string{"linux", "macos", "windows"}

// dimValueMatches decides one pinned dimension. Every dimension is exact
// except the OS, where a family name also matches every distribution in
// it: pinning "linux" keeps alpine musl and debian glibc, while pinning
// "alpine musl" keeps only that one.
func dimValueMatches(dim, want, got string) bool {
	if want == "" {
		return true
	}
	if want == got {
		return true
	}
	if dim != "os" || got == "" {
		return false
	}
	for _, family := range osFamilies {
		if want == family {
			icon, _ := osIcon(got)
			return icon == family
		}
	}
	return false
}

// filterCubeFacts keeps the facts matching every pinned dimension.
func filterCubeFacts(facts []cubeFact, filters map[string]string) []cubeFact {
	if len(filters) == 0 {
		return facts
	}
	var out []cubeFact
	for _, f := range facts {
		keep := true
		for dim, want := range filters {
			if !dimValueMatches(dim, want, f.Dims[dim]) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, f)
		}
	}
	return out
}

// osFilterValues lists the OS filter's choices: each platform present,
// then the exact recorded environments inside it.
func cubeOSFilterOptions(exact []string) []cubeFilterOption {
	seen := map[string]bool{}
	for _, v := range exact {
		if icon, _ := osIcon(v); icon != "" {
			seen[icon] = true
		}
	}
	var out []cubeFilterOption
	for _, family := range osFamilies {
		if !seen[family] {
			continue
		}
		out = append(out, cubeFilterOption{Value: family, Label: family})
		for _, v := range exact {
			if icon, _ := osIcon(v); icon == family && v != family {
				out = append(out, cubeFilterOption{Value: v, Label: "  " + v})
			}
		}
	}
	// Anything the family test did not recognise still has to be
	// selectable; nothing recorded may become unreachable.
	for _, v := range exact {
		if icon, _ := osIcon(v); icon == "" {
			out = append(out, cubeFilterOption{Value: v, Label: v})
		}
	}
	return out
}

// cubeDimValues returns the distinct recorded values of one dimension.
func cubeDimValues(facts []cubeFact, dim string) []string {
	seen := map[string]bool{}
	for _, f := range facts {
		if v := f.Dims[dim]; v != "" {
			seen[v] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	return sortCubeDimValues(dim, out)
}

// cubeAxisValues is cubeDimValues restricted to the evidence an axis on that
// dimension would actually render.
//
// A grid spread over WHERE things ran drops this network's own runs (see
// observationsOnlyOnEnvironmentAxes), so a coordinate only the farm has ever
// executed carries a value on every environment dimension and evidence on
// none of them. Deciding the axes from the raw facts therefore picked one
// and rendered an empty shell: gem/rack-test, with 21 samples and 19 signed
// PASS receipts, opened on a blank grid, and 726 of the 2,362 public samples
// sit on a coordinate shaped exactly like it.
//
// An axis has to spread the evidence it will show. That is the rule the
// symbol axis already gets; this applies it to the environment dimensions.
func cubeAxisValues(facts []cubeFact, dim string) []string {
	if !isEnvironmentDim(dim) {
		return cubeDimValues(facts, dim)
	}
	kept := make([]cubeFact, 0, len(facts))
	for _, f := range facts {
		if f.Agg.observationPart().events() > 0 {
			kept = append(kept, f)
		}
	}
	return cubeDimValues(kept, dim)
}

// defaultCubeAxes picks the slice's axes: the highest-priority unpinned
// dimensions that still vary. ok is false when at most one measured
// combination remains — the caller renders the leaf record instead.
func defaultCubeAxes(facts []cubeFact, pinned map[string]string) (x, y string, ok bool) {
	varies := map[string]bool{}
	for _, dim := range cubeDimKeys {
		// A symbol axis whose only value is the package-level aggregate is
		// not a symbol axis. It renders one row reading "whole package",
		// which is the total with none of its parts beside it -- a grid that
		// says nothing a single number would not.
		if dim == "symbol" && !cubeHasRealSymbol(facts) {
			continue
		}
		// An axis has to spread. The symbol dimension counts "whole package"
		// among its values, so one real symbol beside the aggregate read as
		// two and qualified — and then the grid dropped the aggregate and
		// rendered a single row. hasown opened exactly that way: one green
		// cell on alpine, with every windows run it has, and every failure,
		// off screen because they were recorded against the package.
		//
		// Counting the symbols that would actually survive the drop is the
		// same rule the other dimensions get, applied to what a symbol axis
		// really holds.
		if dim == "symbol" {
			if len(cubeRealSymbols(facts)) >= 2 {
				varies[dim] = true
			}
			continue
		}
		// Whether a dimension can be an axis is decided by the slice, not
		// by the filter list: pinning the OS to a whole platform still
		// leaves alpine musl and debian glibc to spread along it, and a
		// dimension pinned to one exact value has one value here anyway.
		if len(cubeAxisValues(facts, dim)) >= 2 {
			varies[dim] = true
		}
	}
	if len(varies) == 0 {
		return "", "", false
	}
	for _, dim := range cubeXAxisPriority {
		if varies[dim] {
			x = dim
			break
		}
	}
	// The partner is chosen against x, not on its own. Whether an axis
	// spreads depends on what sits beside it, so asking each dimension in
	// isolation picked partners that erase the axis they were paired with.
	y, ownSpread := cubePartnerAxis(facts, x, varies, pinned, true)
	if y == "" {
		// No partner leaves x standing. Take one that does not rather than
		// reporting a bottomed-out drill-down: ok == false tells the caller
		// the coordinate is DECIDED, and a slice that still spreads would
		// then be stated as one answer it has not earned.
		y, ownSpread = cubePartnerAxis(facts, x, varies, pinned, false)
	}
	// A partner with no spread of its own is there to give the grid a second
	// axis, not because it was the question. Which of the two goes across is
	// then the priority lists' call -- version across, symbol down -- and
	// deciding it by which dimension happened to vary turned the page's
	// symbol rows into symbol columns the moment a version was pinned.
	if !ownSpread && y != "" && cubeAxesReadBetterTransposed(x, y) {
		x, y = y, x
	}
	if x == "" || y == "" {
		return "", "", false
	}
	return x, y, true
}

// cubeAxisSpread counts the values a dimension would actually render on a
// grid spread over dim × other.
//
// It is cubeAxisValues asked about a PAIR, and the pair is the honest unit:
// a grid with an environment dimension on either axis drops this network's
// own runs (observationsOnlyOnEnvironmentAxes), so how much of one axis
// survives is decided by the dimension beside it, not by that axis alone.
func cubeAxisSpread(facts []cubeFact, dim, other string) int {
	rendered := observationsOnlyOnEnvironmentAxes(cubeFactsOnAxes(facts, dim, other), dim, other)
	if dim == "symbol" {
		return len(cubeRealSymbols(rendered))
	}
	return len(cubeDimValues(rendered, dim))
}

// cubePartnerAxis picks the second axis for a slice whose first is settled.
// ownSpread reports whether the partner holds a spread of its own or is
// there only so the slice renders as a (1×N) grid.
//
// Preference order: a dimension that spreads, then one that does not, and
// last one the reader pinned — whose header can only restate the pin, and is
// still better than a grid holding none of the evidence it was opened for.
//
// strict rejects a partner that would erase x. cargo/tokio at 1.53.1 is the
// case that named this: with the version pinned the only dimension still
// spreading is the symbol, its evidence is contract receipts, and an OS
// partner drops every one of them. The grid came out one cell wide holding
// the package-level total, that cell lost its link because nothing outside
// the two axes still varied, and the five measured APIs underneath were
// unreachable by clicking anything on the page.
func cubePartnerAxis(facts []cubeFact, x string, varies map[string]bool,
	pinned map[string]string, strict bool) (dim string, ownSpread bool) {

	// Two values is the bar the axis itself had to clear to be chosen, so it
	// is the bar a partner must leave it above. Not "every value it had": an
	// OS grid answers where builds ran and is entitled to hold fewer symbols
	// than the version axis does. What it may not do is hold none of them.
	survives := func(d string) bool {
		return !strict || cubeAxisSpread(facts, x, d) >= 2
	}
	for _, d := range cubeYAxisPriority {
		if d != x && varies[d] && survives(d) {
			return d, true
		}
	}
	for _, allowPinned := range []bool{false, true} {
		for _, d := range cubeYAxisPriority {
			if d == x || varies[d] {
				continue
			}
			if _, isPinned := pinned[d]; isPinned != allowPinned {
				continue
			}
			if d == "symbol" && !cubeHasRealSymbol(facts) {
				continue
			}
			if len(cubeAxisValues(facts, d)) == 0 || !survives(d) {
				continue
			}
			return d, false
		}
	}
	return "", false
}

// cubeAxesReadBetterTransposed reports whether a pair belongs the other way
// round, by the only statement this file makes about where a dimension
// belongs: the two priority lists.
func cubeAxesReadBetterTransposed(x, y string) bool {
	rank := func(list []string, dim string) int {
		for i, d := range list {
			if d == dim {
				return i
			}
		}
		return len(list)
	}
	as := rank(cubeXAxisPriority, x) + rank(cubeYAxisPriority, y)
	swapped := rank(cubeXAxisPriority, y) + rank(cubeYAxisPriority, x)
	return swapped < as
}

// buildCubeGrid pivots a fact slice on two dimensions. Facts that never
// recorded either dimension stay out — the cube does not guess.
// cubeFactsOnAxes is the evidence a grid on these axes actually renders: a
// fact missing either coordinate has no cell to sit in.
//
// The package-level aggregate used to be dropped from a symbol axis, because
// it is the total OVER the symbols and sat among its own parts carrying all
// the numbers. Dropping it cost more than it saved: yaml is measured at
// symbol grain on alpine and at package grain on windows, where all 42 of its
// failure clusters were recorded, so the page opened on six green rows for a
// package with 42 recorded failures. It is kept and marked as a total —
// sorted below its parts, outside the tallies.
// observationsOnlyOnEnvironmentAxes drops this network's own runs from a
// grid spread over WHERE things ran.
//
// Every receipt this network holds is signed inside a linux container, so
// keying verification by environment put the check in the linux cell and
// left every other cell of the row reading as "not verified there". That is
// a per-platform verdict, and this network does not offer one: it offers a
// sample that builds. A sample answers one version of one API — which OS
// the container happened to be is not part of the claim.
//
// So an environment grid answers the question it is actually about: where
// did builds run, and how did they go. The verification is not lost — the
// version and symbol axes still carry it, the exact records still state the
// environment each run happened in, and the sample page counts the signing
// keys that built it.
func observationsOnlyOnEnvironmentAxes(facts []cubeFact, x, y string) []cubeFact {
	if !isEnvironmentDim(x) && !isEnvironmentDim(y) {
		return facts
	}
	out := make([]cubeFact, 0, len(facts))
	for _, f := range facts {
		f.Agg = f.Agg.observationPart()
		if f.Agg.events() == 0 {
			continue
		}
		out = append(out, f)
	}
	return out
}

// isEnvironmentDim reports whether a cube dimension describes WHERE a run
// happened rather than WHAT it was about. Version and symbol are what; the
// rest are where. An empty axis is neither — the bottomed-out view has no
// grid at all.
func isEnvironmentDim(dim string) bool {
	return dim != "" && dim != "version" && dim != "symbol"
}

// cubeFactsWithoutTotal removes the package-level aggregate from a symbol
// axis. It is the right thing where the grid answers a question about the
// symbols themselves — "which symbol ran where" on the version page, and the
// hero, which must not open on a row that is not one of the parts beside it.
//
// The package cube keeps it: that grid is where a reader drills, and the
// failures of a package measured mainly at package grain live nowhere else.
func cubeFactsWithoutTotal(facts []cubeFact, x, y string) []cubeFact {
	if x != "symbol" && y != "symbol" {
		return facts
	}
	out := make([]cubeFact, 0, len(facts))
	for _, f := range facts {
		if f.Dims["symbol"] == cubePackageLevel || f.PackageLevel {
			continue
		}
		out = append(out, f)
	}
	return out
}

func cubeFactsOnAxes(facts []cubeFact, x, y string) []cubeFact {
	out := make([]cubeFact, 0, len(facts))
	for _, f := range facts {
		// No axis means no cell to be missing from: the bottomed-out view has
		// no grid, and requiring coordinates there dropped every fact it had.
		if (x != "" && f.Dims[x] == "") || (y != "" && f.Dims[y] == "") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// keepEmptyAxisValues puts back the rows and columns the symbol drop emptied.
//
// hasown was measured at two versions but only 2.0.4 at symbol grain, so
// spreading version by symbol produced one column and the version axis simply
// did not offer 2.0.3 — a version the package has, gone from the only control
// that selects one. The column stays and its cell is empty, which is exactly
// what it is: nothing measured at this grain, not nothing measured.
//
// Only the non-symbol axis: a symbol that was never recorded has no row to be
// missing, and the package-level aggregate is deliberately not a symbol.
func keepEmptyAxisValues(aggs map[cellKey]*pivotAgg, facts []cubeFact, x, y string) {
	if len(aggs) == 0 {
		return
	}
	var anyRow, anyCol string
	for k := range aggs {
		anyRow, anyCol = k.row, k.col
		break
	}
	seen := func(pick func(cellKey) string) map[string]bool {
		out := map[string]bool{}
		for k := range aggs {
			out[pick(k)] = true
		}
		return out
	}
	if x != "symbol" {
		have := seen(func(k cellKey) string { return k.col })
		for _, v := range cubeDimValues(facts, x) {
			if !have[v] {
				aggs[cellKey{anyRow, v}] = &pivotAgg{}
			}
		}
	}
	if y != "symbol" {
		have := seen(func(k cellKey) string { return k.row })
		for _, v := range cubeDimValues(facts, y) {
			if !have[v] {
				aggs[cellKey{v, anyCol}] = &pivotAgg{}
			}
		}
	}
}

func buildCubeGrid(facts []cubeFact, x, y string,
	links pivotLinks, now time.Time, withTotal bool) pivotGrid {

	onAxes := cubeFactsOnAxes(facts, x, y)
	if !withTotal {
		onAxes = cubeFactsWithoutTotal(onAxes, x, y)
	}
	onAxes = observationsOnlyOnEnvironmentAxes(onAxes, x, y)
	cells := map[cellKey][]cubeFact{}
	for _, f := range onAxes {
		cells[cellKey{f.Dims[y], f.Dims[x]}] = append(cells[cellKey{f.Dims[y], f.Dims[x]}], f)
	}
	aggs := make(map[cellKey]*pivotAgg, len(cells))
	for key, cellFacts := range cells {
		aggs[key] = mergeCubeFacts(cellFacts)
	}
	keepEmptyAxisValues(aggs, facts, x, y)
	sortRows := func(vals []string) []string { return sortCubeDimValues(y, vals) }
	sortCols := func(vals []string) []string { return sortCubeDimValues(x, vals) }
	// The package-level row is a total over the symbols, not one of them.
	aggLabel := ""
	if withTotal && (x == "symbol" || y == "symbol") {
		aggLabel = cubePackageLevel
	}
	g := assembleGrid(aggs, sortRows, sortCols, y == "os", x == "os", links, now, aggLabel)
	return dropLinksThatShowNothingNew(g, x, y, anyDimStillVaries(facts, x, y))
}

// dropLinksThatShowNothingNew strips the links on a one-cell grid that cannot
// take the reader anywhere.
//
// One row and one column means pinning either narrows to the slice already on
// screen. That was the symbol row: a link whose destination was the page it
// was on.
//
// But a grid narrow in ITS TWO AXES is not a decided coordinate. hasown spread
// runtime by symbol renders one cell while the version is still undecided, and
// stripping the link there left the reader on a hub with no way in — which is
// why the page ended up showing every version's failures at once and reading
// as a pile. While any other dimension still varies the cell is a door: it
// pins both axes and the next view re-spreads over what is left.
//
// The version axis is a door on the same principle: pinning a version opens
// that release's own dependency list, which the unpinned page cannot show.
func dropLinksThatShowNothingNew(g pivotGrid, x, y string, moreToPin bool) pivotGrid {
	if len(g.Rows) != 1 || len(g.Cols) != 1 {
		return g
	}
	// A header pins one axis into a slice whose other axis has a single value
	// anyway, so it lands exactly where the cell lands. Two links to one
	// destination read as two choices, and the label looks like a link that
	// goes nowhere new. The cell is the door; the version header is the one
	// exception, because a version is a thing a reader wants to hold alone.
	if x != "version" {
		g.Cols[0].Href = ""
	}
	if y != "version" {
		g.Rows[0].Href = ""
	}
	if !moreToPin {
		g.Rows[0].Cells[0].Href = ""
	}
	return g
}

// anyDimStillVaries reports whether pinning both axes would leave the reader
// anything further to narrow — the test for whether a one-cell grid is a dead
// end or a step on the way to a decided coordinate.
func anyDimStillVaries(facts []cubeFact, x, y string) bool {
	for _, dim := range cubeDimKeys {
		if dim == x || dim == y {
			continue
		}
		if len(cubeDimValues(facts, dim)) >= 2 {
			return true
		}
	}
	return false
}

// mergeCubeFacts folds one cell's facts together without double-counting.
//
// Within one (version, environment bucket) exactly ONE fact contributes, and
// that now holds for observations as well as verifications.
//
// It was only ever applied to verifications, on the stated belief that
// observations are disjoint per symbol. They are not: the recorder writes a
// package-level observation for a build AND one for every symbol detected in
// that same build (internal/evidence/recorder.go), so summing them counts one
// build once for the package and again for each symbol. A cell on a
// non-symbol axis therefore reported far more runs than ever happened -- the
// same inflation the network-wide evidence counter carried until it was
// changed to count package-level rows only.
//
// The package-level fact is the superset and wins. Failing that, the symbol
// fact with the most events is a safe lower bound: the others are copies of
// the same builds.
func mergeCubeFacts(facts []cubeFact) *pivotAgg {
	agg := &pivotAgg{}
	type verKey struct{ version, envHash string }
	pick := func(cur cubeFact, ok bool, f cubeFact, weight func(pivotAgg) int64) bool {
		switch {
		case !ok:
			return true
		case f.PackageLevel && !cur.PackageLevel:
			return true
		case f.PackageLevel == cur.PackageLevel && weight(f.Agg) > weight(cur.Agg):
			return true
		}
		return false
	}
	// used counts on the observed side: presence is an observation too, and
	// the recorder writes it package-level AND per symbol from the same scan,
	// so it needs the same one-fact-per-bucket fold. Gating on run outcomes
	// alone merged a usage-only coordinate to a zero aggregate, and the cube
	// rendered a package seen in hundreds of projects as "never measured" —
	// the invariant buildPivot already keeps.
	obsWeight := func(a pivotAgg) int64 { return a.obsPass + a.obsFail + a.used }
	verWeight := func(a pivotAgg) int64 { return a.verPass + a.verFail }

	observed := map[verKey]cubeFact{}
	verified := map[verKey]cubeFact{}
	for _, f := range facts {
		key := verKey{f.Dims["version"], f.EnvHash}
		if obsWeight(f.Agg) > 0 {
			if cur, ok := observed[key]; pick(cur, ok, f, obsWeight) {
				observed[key] = f
			}
		}
		if verWeight(f.Agg) > 0 || f.Agg.cross {
			if cur, ok := verified[key]; pick(cur, ok, f, verWeight) {
				verified[key] = f
			}
		}
	}
	for _, f := range observed {
		agg.mergeObservations(f.Agg)
	}
	for _, f := range verified {
		agg.mergeVerifications(f.Agg)
	}
	return agg
}

// sortCubeDimValues orders one dimension's values for display: versions
// newest first, operating systems in the familiar order, everything else
// like context labels (line alphabetical, newest major first).
func sortCubeDimValues(dim string, vals []string) []string {
	switch dim {
	case "version":
		sorted := append([]string(nil), vals...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if c := domain.CompareVersions(sorted[i], sorted[j]); c != 0 {
				return c > 0
			}
			return sorted[i] < sorted[j]
		})
		return sorted
	case "os":
		return sortPivotRows(vals)
	case "symbol":
		// The package-level row is not a symbol and must not sort among them.
		// Alphabetically it landed after ParseConfig, so the aggregate over
		// every symbol appeared partway down the list of its own parts.
		sorted := sortPivotCols(vals)
		out := make([]string, 0, len(sorted))
		for _, v := range sorted {
			if v == cubePackageLevel {
				out = append(out, v)
			}
		}
		for _, v := range sorted {
			if v != cubePackageLevel {
				out = append(out, v)
			}
		}
		return out
	default:
		return sortPivotCols(vals)
	}
}

// ---------------------------------------------------------------------------
// Per-process cube cache: assembling a cube reads up to
// cubeMaxVersions × (cubeMaxSymbolsPerVersion + 1) snapshots, which is fine
// on a timer and not fine on every request.

type cubeCacheEntry struct {
	facts    []cubeFact
	windowed bool
	at       time.Time
}

// cubeFactsCached reports an already-assembled cube, and never assembles
// one. It exists so a page can prefer what is warm over what is best: the
// landing hero ranks candidates by grid richness, and doing that by loading
// each candidate put the whole fan-out on the request path.
func (s *site) cubeFactsCached(eco, name string) ([]cubeFact, bool) {
	s.cubeMu.Lock()
	defer s.cubeMu.Unlock()
	e, ok := s.cubeCache[eco+"|"+name]
	if !ok || time.Since(e.at) >= cubeTTL {
		return nil, false
	}
	return e.facts, true
}

func (s *site) cubeFacts(ctx context.Context, eco, name string) ([]cubeFact, bool) {
	key := eco + "|" + name
	for {
		now := time.Now()
		s.cubeMu.Lock()
		if e, ok := s.cubeCache[key]; ok && now.Sub(e.at) < cubeTTL {
			s.cubeMu.Unlock()
			return e.facts, e.windowed
		}
		// Someone is already assembling this package. Wait for them instead of
		// repeating dozens of round trips: the production pool is eight
		// connections, so a stampede on one cold cube is what stops every other
		// page rather than merely slowing this one.
		if wait, loading := s.cubeLoading[key]; loading {
			s.cubeMu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				return nil, false
			}
			// Re-read: normally the loader has just filled the cache. If it
			// failed it left nothing, and this waiter takes its turn loading.
			continue
		}
		if s.cubeLoading == nil {
			s.cubeLoading = map[string]chan struct{}{}
		}
		done := make(chan struct{})
		s.cubeLoading[key] = done
		s.cubeMu.Unlock()

		// The key is released even if the load panics. A handler panic is
		// recovered upstream (web.go handle), so the process survives -- and
		// without this the key would keep pointing at a channel nobody closes,
		// parking every later reader until its request context expires and
		// taking that package's cube out of the site until a restart.
		facts, windowed, err := func() ([]cubeFact, bool, error) {
			defer func() {
				s.cubeMu.Lock()
				delete(s.cubeLoading, key)
				close(done)
				s.cubeMu.Unlock()
			}()
			// The assembly is shared, not this request's private work: readers
			// are parked on it and the next one is served whatever it caches. Tied
			// to the initiating context, one reader pressing stop mid fan-out
			// yields a partial assembly — loadCubeFacts swallows per-hop failures
			// on purpose, so cancellation arrives as emptiness rather than an
			// error — and that emptiness gets cached for everyone for cubeTTL.
			loadCtx, done := context.WithTimeout(context.WithoutCancel(ctx), cubeLoadTimeout)
			defer done()
			return loadCubeFacts(loadCtx, s.d.Store, eco, name)
		}()
		if err != nil {
			return nil, false
		}

		s.cubeMu.Lock()
		if s.cubeCache == nil {
			s.cubeCache = map[string]cubeCacheEntry{}
		}
		if len(s.cubeCache) >= cubeCacheMax {
			// Expired entries go first; only if none were expired does one
			// arbitrary fresh entry make room.
			for k, e := range s.cubeCache {
				if now.Sub(e.at) >= cubeTTL {
					delete(s.cubeCache, k)
				}
			}
			if len(s.cubeCache) >= cubeCacheMax {
				for k := range s.cubeCache {
					delete(s.cubeCache, k)
					break
				}
			}
		}
		s.cubeCache[key] = cubeCacheEntry{facts: facts, windowed: windowed, at: now}
		s.cubeMu.Unlock()
		return facts, windowed
	}
}

// cubeHasRealSymbol reports whether the slice knows any symbol beyond the
// package-level aggregate. Without one, "symbol" is a dimension with a single
// synthetic value and must not be offered as an axis.
// cubeRealSymbols lists the symbols on a slice, without the package-level
// total. The total is a value of the symbol dimension but not a symbol, so
// counting it made one measured API look like a spread of two.
func cubeRealSymbols(facts []cubeFact) []string {
	seen := map[string]bool{}
	for _, f := range facts {
		if sym := f.Dims["symbol"]; sym != "" && sym != cubePackageLevel && !f.PackageLevel {
			seen[sym] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	return out
}

func cubeHasRealSymbol(facts []cubeFact) bool {
	for _, f := range facts {
		if sym := f.Dims["symbol"]; sym != "" && sym != cubePackageLevel {
			return true
		}
	}
	return false
}
