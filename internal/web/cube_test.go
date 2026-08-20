package web

import (
	"context"
	"strings"
	"testing"
)

// cubeSnap builds a one-row snapshot JSON with a full envBucket, the way
// the producer materializes it.
func cubeSnap(purl, symbol, os, arch, runtime, runtimeVersion, pm, stage string, pass, fail int64) string {
	return `{
	  "schemaVersion": 1,
	  "purl": "` + purl + `",
	  "symbol": "` + symbol + `",
	  "generatedAt": "2026-08-13T00:00:00Z",
	  "rows": [{
	    "envBucket": {"schemaVersion":1,"os":"` + os + `","arch":"` + arch + `",
	      "runtime":"` + runtime + `","runtimeVersion":"` + runtimeVersion + `",
	      "packageManager":"` + pm + `"},
	    "confidence": "MEDIUM",
	    "passRate": 1,
	    "lastSeen": "2026-08-12T10:00:00Z",
	    "byStage": {"` + stage + `": {"pass": ` + itoa(pass) + `, "fail": ` + itoa(fail) + `}}
	  }],
	  "failures": []
	}`
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// newCubeStore seeds a store with two versions × (package-level + two
// symbols) of npm/react-like data spanning several dimensions.
func newCubeStore() *fakeStore {
	f := newFakeStore()
	f.versions["npm|reactish"] = []string{"19.1.0", "18.3.1"}
	f.symbols["npm|reactish|19.1.0"] = []string{"createRoot", "hydrateRoot"}
	f.symbols["npm|reactish|18.3.1"] = []string{"createRoot"}
	f.snapshots[snapKey("pkg:npm/reactish@19.1.0", "")] =
		cubeSnap("pkg:npm/reactish@19.1.0", "", "linux", "x64", "node", "22.14", "npm", "PROJECT_COMPILE", 30, 0)
	f.snapshots[snapKey("pkg:npm/reactish@19.1.0", "createRoot")] =
		cubeSnap("pkg:npm/reactish@19.1.0", "createRoot", "linux", "x64", "node", "22.14", "npm", "CONTRACT", 4, 0)
	f.snapshots[snapKey("pkg:npm/reactish@19.1.0", "hydrateRoot")] =
		cubeSnap("pkg:npm/reactish@19.1.0", "hydrateRoot", "windows", "x64", "node", "22.14", "pnpm", "CONTRACT", 0, 2)
	f.snapshots[snapKey("pkg:npm/reactish@18.3.1", "")] =
		cubeSnap("pkg:npm/reactish@18.3.1", "", "windows", "arm64", "node", "20.9", "npm", "PROJECT_COMPILE", 9, 1)
	f.snapshots[snapKey("pkg:npm/reactish@18.3.1", "createRoot")] =
		cubeSnap("pkg:npm/reactish@18.3.1", "createRoot", "darwin", "arm64", "node", "20.9", "npm", "CONTRACT", 2, 0)
	return f
}

// Facts are tagged with every dimension they came from: version, symbol
// and the bucketed environment axes. The package-level symbol "" is its
// own disjoint evidence, not a duplicate of the per-symbol rows.
func TestCubeFactsTagSourceDims(t *testing.T) {
	f := newCubeStore()
	facts, _, err := loadCubeFacts(context.Background(), f, "npm", "reactish")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 5 {
		t.Fatalf("facts = %d, want 5 (2 package-level + 3 symbol snapshots)", len(facts))
	}
	var hydrate *cubeFact
	for i := range facts {
		if facts[i].Dims["symbol"] == "hydrateRoot" {
			hydrate = &facts[i]
		}
	}
	if hydrate == nil {
		t.Fatal("no fact for hydrateRoot")
	}
	want := map[string]string{
		"version": "19.1.0", "symbol": "hydrateRoot", "os": "windows",
		"arch": "x64", "runtime": "node 22", "tool": "pnpm",
	}
	for k, v := range want {
		if hydrate.Dims[k] != v {
			t.Errorf("hydrateRoot dim %s = %q, want %q", k, hydrate.Dims[k], v)
		}
	}
	if hydrate.Agg.verFail != 2 {
		t.Errorf("hydrateRoot verFail = %d, want 2", hydrate.Agg.verFail)
	}
}

// The cube reads at most cubeMaxVersions versions and
// cubeMaxSymbolsPerVersion symbols per version.
func TestCubeLoadCapsReads(t *testing.T) {
	f := newCubeStore()
	var versions []string
	for i := 0; i < 12; i++ {
		v := "9." + itoa(int64(12-i)) + ".0"
		versions = append(versions, v)
		purl := "pkg:npm/reactish@" + v
		f.snapshots[snapKey(purl, "")] =
			cubeSnap(purl, "", "linux", "x64", "node", "22.1", "npm", "PROJECT_COMPILE", 3, 0)
	}
	f.versions["npm|reactish"] = versions
	facts, windowed, err := loadCubeFacts(context.Background(), f, "npm", "reactish")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, fact := range facts {
		seen[fact.Dims["version"]] = true
	}
	if len(seen) != cubeMaxVersions {
		t.Errorf("cube read %d versions, want exactly the newest %d", len(seen), cubeMaxVersions)
	}
	if !seen["9.12.0"] || seen["9.1.0"] {
		t.Errorf("cap must keep the newest versions: got %v", seen)
	}
	if !windowed {
		t.Error("a capped assembly must report its window — silent caps mislead")
	}
}

// Filters narrow the slice; the grid pivots the remainder on the chosen
// axes and skips facts that never recorded an axis dimension.
func TestCubeGridSlicesByFilter(t *testing.T) {
	f := newCubeStore()
	facts, _, err := loadCubeFacts(context.Background(), f, "npm", "reactish")
	if err != nil {
		t.Fatal(err)
	}
	sliced := filterCubeFacts(facts, map[string]string{"os": "windows"})
	g := buildCubeGrid(sliced, "version", "arch", pivotLinks{}, pivotNow)
	if len(g.Cols) != 2 || g.Cols[0].Label != "19.1.0" || g.Cols[1].Label != "18.3.1" {
		t.Fatalf("cols = %v, want versions newest first", g.Cols)
	}
	c := cellAt(t, g, "x64", "19.1.0")
	// Verified with no observation: the mark stays, and the count says no
	// usage was recorded rather than claiming zero machines got through.
	if c.Basis != "verified" || c.Ratio != "—" || c.Glyph != "" {
		t.Errorf("windows/x64/19.1.0 = %q %q glyph=%q, want verified with no usage",
			c.Basis, c.Ratio, c.Glyph)
	}
	if got := cellAt(t, g, "arm64", "18.3.1").Basis; got != "observed" {
		t.Errorf("windows/arm64/18.3.1 = %q, want OBSERVED", got)
	}
}

// Default axes follow the priority lists over dimensions that still vary:
// the first unfiltered slice of this store pivots version × symbol.
func TestCubeDefaultAxes(t *testing.T) {
	f := newCubeStore()
	facts, _, _ := loadCubeFacts(context.Background(), f, "npm", "reactish")
	x, y, ok := defaultCubeAxes(facts, nil)
	if !ok || x != "version" || y != "symbol" {
		t.Fatalf("axes = %s × %s ok=%v, want version × symbol", x, y, ok)
	}
	// Pin both: version still varies inside windows/node 22? No — drill to
	// a slice where only one combination remains and the cube says leaf.
	leafSlice := filterCubeFacts(facts, map[string]string{
		"os": "windows", "runtime": "node 22",
	})
	if _, _, ok := defaultCubeAxes(leafSlice, map[string]string{"os": "", "runtime": ""}); ok {
		t.Error("a single-combination slice must report leaf, not axes")
	}
}

// The version dimension sorts newest first wherever it appears, with
// prereleases BELOW the release they precede — the same ordering the
// versions list on the very same page uses (domain.CompareVersions).
func TestCubeVersionAxisSortsNewestFirst(t *testing.T) {
	got := sortCubeDimValues("version", []string{"1.9.0", "1.12.0", "2.0.0", "v1.2.0"})
	want := "2.0.0,1.12.0,1.9.0,v1.2.0"
	if strings.Join(got, ",") != want {
		t.Errorf("sorted = %v, want %s", got, want)
	}
	got = sortCubeDimValues("version", []string{"19.0.0-rc.1", "19.0.0", "18.3.1"})
	want = "19.0.0,19.0.0-rc.1,18.3.1"
	if strings.Join(got, ",") != want {
		t.Errorf("prerelease sorted = %v, want %s", got, want)
	}
}

// The producer files the SAME verification receipt into the package-level
// snapshot and every claimed symbol's snapshot. When a grid merges across
// the symbol dimension, one contract run must count once — never once per
// symbol it was filed under.
func TestCubeGridDedupesDuplicatedVerifications(t *testing.T) {
	f := newFakeStore()
	f.versions["npm|dup"] = []string{"1.0.0"}
	f.symbols["npm|dup|1.0.0"] = []string{"a", "b"}
	// Identical environment bucket in all three snapshots; the "" snapshot
	// carries the superset (the same single receipt).
	for _, sym := range []string{"", "a", "b"} {
		f.snapshots[snapKey("pkg:npm/dup@1.0.0", sym)] =
			cubeSnap("pkg:npm/dup@1.0.0", sym, "linux", "x64", "node", "22.1", "npm", "CONTRACT", 1, 0)
	}
	facts, _, err := loadCubeFacts(context.Background(), f, "npm", "dup")
	if err != nil {
		t.Fatal(err)
	}
	g := buildCubeGrid(facts, "runtime", "os", pivotLinks{}, pivotNow)
	c := cellAt(t, g, "linux", "node 22")
	if c.Ver != 1 {
		t.Fatalf("cell ver = %d, want the one real contract run counted once", c.Ver)
	}
	// "1/1" is deliberate. Suppressing the rate on a single run rendered it
	// identically to a hundred agreeing runs, which is the overstatement the
	// rate exists to prevent -- how thin the evidence is IS the measurement.
	// One clean contract run and no observations: the mark is the fact, and
	// the count reports that nobody has been seen using it.
	if c.Glyph != "✓" || c.Ratio != "—" {
		t.Errorf("deduped run = glyph %q count %q, want the mark and no usage",
			c.Glyph, c.Ratio)
	}
}

// Distinct receipts in DIFFERENT environment buckets still all count.
func TestCubeGridKeepsDistinctVerifications(t *testing.T) {
	f := newFakeStore()
	f.versions["npm|dup"] = []string{"1.0.0"}
	f.symbols["npm|dup|1.0.0"] = []string{"a"}
	f.snapshots[snapKey("pkg:npm/dup@1.0.0", "")] =
		cubeSnap("pkg:npm/dup@1.0.0", "", "linux", "x64", "node", "22.1", "npm", "CONTRACT", 1, 0)
	f.snapshots[snapKey("pkg:npm/dup@1.0.0", "a")] =
		cubeSnap("pkg:npm/dup@1.0.0", "a", "linux", "arm64", "node", "22.1", "npm", "CONTRACT", 1, 0)
	facts, _, err := loadCubeFacts(context.Background(), f, "npm", "dup")
	if err != nil {
		t.Fatal(err)
	}
	g := buildCubeGrid(facts, "runtime", "os", pivotLinks{}, pivotNow)
	if got := cellAt(t, g, "linux", "node 22").Ver; got != 2 {
		t.Fatalf("cell ver = %d, want 2 — different env buckets are different runs", got)
	}
}

// The cube's default axes decide whether a reader sees a grid or a diagonal.
//
// They used to be runtime × OS, which on this corpus guarantees emptiness:
// every observation is recorded on Windows and every verification runs on
// Linux, so splitting by OS puts the two halves in different rows and no cell
// ever holds both. Version × symbol is also the question the site exists to
// answer — does this API work in this release — and it is dense.
func TestCubeDefaultsToVersionBySymbol(t *testing.T) {
	facts := []cubeFact{}
	for _, version := range []string{"1.0.0", "1.1.0"} {
		for _, symbol := range []string{"a.Call", "b.Call"} {
			for _, os := range []string{"linux", "windows"} {
				facts = append(facts, cubeFact{Dims: map[string]string{
					"version": version, "symbol": symbol, "os": os,
					"runtime": "node 22", "arch": "x64",
				}})
			}
		}
	}
	x, y, ok := defaultCubeAxes(facts, nil)
	if !ok {
		t.Fatal("no axes chosen for a cube that varies in four dimensions")
	}
	if x != "version" || y != "symbol" {
		t.Errorf("axes = %s × %s, want version × symbol", x, y)
	}
}

// With one release there is nothing to compare along the version axis, so it
// must fall through rather than render a single column.
func TestCubeFallsBackWhenVersionDoesNotVary(t *testing.T) {
	facts := []cubeFact{}
	for _, symbol := range []string{"a.Call", "b.Call"} {
		for _, runtime := range []string{"node 20", "node 22"} {
			facts = append(facts, cubeFact{Dims: map[string]string{
				"version": "1.0.0", "symbol": symbol, "runtime": runtime, "os": "linux",
			}})
		}
	}
	x, y, ok := defaultCubeAxes(facts, nil)
	if !ok {
		t.Fatal("no axes chosen")
	}
	if x == "version" {
		t.Errorf("x = %s; a single release is not an axis", x)
	}
	if y != "symbol" {
		t.Errorf("y = %s, want symbol", y)
	}
}
