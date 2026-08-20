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
	cubePackageLevel = "(package)"

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
			rt := e.Runtime
			if e.RuntimeVersion != "" {
				rt += " " + majorOf(e.RuntimeVersion)
			}
			dims["runtime"] = rt
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

// defaultCubeAxes picks the slice's axes: the highest-priority unpinned
// dimensions that still vary. ok is false when at most one measured
// combination remains — the caller renders the leaf record instead.
func defaultCubeAxes(facts []cubeFact, pinned map[string]string) (x, y string, ok bool) {
	varies := map[string]bool{}
	for _, dim := range cubeDimKeys {
		// Whether a dimension can be an axis is decided by the slice, not
		// by the filter list: pinning the OS to a whole platform still
		// leaves alpine musl and debian glibc to spread along it, and a
		// dimension pinned to one exact value has one value here anyway.
		if len(cubeDimValues(facts, dim)) >= 2 {
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
	for _, dim := range cubeYAxisPriority {
		if varies[dim] && dim != x {
			y = dim
			break
		}
	}
	// One varying dimension: pair it with the best single-valued one so
	// the slice still renders as a (1×N) grid.
	if y == "" {
		for _, dim := range cubeYAxisPriority {
			if dim == x {
				continue
			}
			if _, isPinned := pinned[dim]; isPinned {
				continue
			}
			if len(cubeDimValues(facts, dim)) >= 1 {
				y = dim
				break
			}
		}
	}
	if x == "" || y == "" {
		return "", "", false
	}
	return x, y, true
}

// buildCubeGrid pivots a fact slice on two dimensions. Facts that never
// recorded either dimension stay out — the cube does not guess.
func buildCubeGrid(facts []cubeFact, x, y string,
	links pivotLinks, now time.Time) pivotGrid {

	cells := map[cellKey][]cubeFact{}
	for _, f := range facts {
		rk, ck := f.Dims[y], f.Dims[x]
		if rk == "" || ck == "" {
			continue
		}
		key := cellKey{rk, ck}
		cells[key] = append(cells[key], f)
	}
	aggs := make(map[cellKey]*pivotAgg, len(cells))
	for key, cellFacts := range cells {
		aggs[key] = mergeCubeFacts(cellFacts)
	}
	sortRows := func(vals []string) []string { return sortCubeDimValues(y, vals) }
	sortCols := func(vals []string) []string { return sortCubeDimValues(x, vals) }
	return assembleGrid(aggs, sortRows, sortCols, y == "os", x == "os", links, now)
}

// mergeCubeFacts folds one cell's facts together without double-counting.
//
// Observations are disjoint per symbol and sum across all facts. The same
// verification receipt, however, is filed by the producer into the
// package-level snapshot AND every claimed symbol's snapshot — so within
// one (version, environment bucket) only ONE fact may contribute
// verification counts: the package-level one (the superset), or failing
// that, the symbol fact with the most verification events (a safe lower
// bound; the duplicates are copies of the same receipts).
func mergeCubeFacts(facts []cubeFact) *pivotAgg {
	agg := &pivotAgg{}
	type verKey struct{ version, envHash string }
	chosen := map[verKey]cubeFact{}
	for _, f := range facts {
		agg.mergeObservations(f.Agg)
		if f.Agg.verPass+f.Agg.verFail == 0 && !f.Agg.cross {
			continue
		}
		key := verKey{f.Dims["version"], f.EnvHash}
		cur, ok := chosen[key]
		switch {
		case !ok:
			chosen[key] = f
		case f.PackageLevel && !cur.PackageLevel:
			chosen[key] = f
		case f.PackageLevel == cur.PackageLevel &&
			f.Agg.verPass+f.Agg.verFail > cur.Agg.verPass+cur.Agg.verFail:
			chosen[key] = f
		}
	}
	for _, f := range chosen {
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
