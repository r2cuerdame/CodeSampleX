package web

import (
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// pivotNow is the fixed reference time every pivot test measures staleness
// against, so a fixture written today does not rot into staleness later.
var pivotNow = time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

// pvRow builds one snapshot row the way the producer would: a bucketed
// environment plus per-stage counts. libc may be "".
func pvRow(os, libc, runtime, runtimeVersion, lastSeen string, byStage map[string]stageCount) snapshotRow {
	env := &domain.EnvironmentFingerprint{
		SchemaVersion: 1, OS: os, Libc: libc,
		Runtime: runtime, RuntimeVersion: runtimeVersion,
	}
	return snapshotRow{
		Env:      env,
		LastSeen: lastSeen,
		ByStage:  byStage,
	}
}

// colLabels joins the grid's column labels for order assertions.
func colLabels(g pivotGrid) string {
	out := make([]string, 0, len(g.Cols))
	for _, c := range g.Cols {
		out = append(out, c.Label)
	}
	return strings.Join(out, ",")
}

func cellAt(t *testing.T, g pivotGrid, row, col string) pivotCell {
	t.Helper()
	ci := -1
	for i, c := range g.Cols {
		if c.Label == col {
			ci = i
		}
	}
	if ci < 0 {
		t.Fatalf("grid has no column %q (cols %v)", col, g.Cols)
	}
	for _, r := range g.Rows {
		if r.Label == row {
			return r.Cells[ci]
		}
	}
	t.Fatalf("grid has no row %q", row)
	return pivotCell{}
}

// Observation counts and verification counts stay separate in a pivot cell,
// exactly as everywhere else on the site (goal.md §3.5).
func TestPivotSeparatesObservationFromVerification(t *testing.T) {
	rows := []snapshotRow{
		pvRow("linux", "", "node", "22.18", "2026-08-12T10:00:00Z", map[string]stageCount{
			"PROJECT_COMPILE": {Pass: 10},
			"CONTRACT":        {Pass: 2},
		}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	c := cellAt(t, g, "linux", "node 22")
	if c.Obs != 10 || c.Ver != 2 {
		t.Fatalf("obs/ver = %d/%d, want 10/2 — never summed", c.Obs, c.Ver)
	}
	if c.Basis != "verified" || c.Class != "verified" {
		t.Errorf("basis = %q/%q, want verified/verified", c.Basis, c.Class)
	}
	if c.Bang {
		t.Errorf("clean verified pass carries an anomaly marker")
	}
}

// A cell with only build observations says so by carrying no verification
// mark. The old "?" said the same thing twice, and its other meaning --
// stale -- no longer applies to a pinned release in a pinned environment.
func TestPivotObservationOnlyCellCarriesNoVerificationMark(t *testing.T) {
	rows := []snapshotRow{
		pvRow("linux", "", "node", "22.18", "2026-08-12T10:00:00Z", map[string]stageCount{
			"PROJECT_COMPILE": {Pass: 40},
		}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	c := cellAt(t, g, "linux", "node 22")
	if c.Basis != "observed" || c.Class != "observed" {
		t.Fatalf("basis = %q/%q, want observed/observed", c.Basis, c.Class)
	}
	if c.Glyph != "" {
		t.Errorf("observation-only cell carries a mark %q; absence is the signal", c.Glyph)
	}
	if c.Bang {
		t.Error("passing observations are not an anomaly")
	}
}

// A verification failure is a measured anomaly: "!" appears. The cell no
// longer says FAIL -- a verdict word claimed more than was measured -- so the
// failure is read off the rate, on a cell whose basis stays "verified".
func TestPivotVerificationFailureIsMarkedBang(t *testing.T) {
	rows := []snapshotRow{
		pvRow("windows", "", "node", "24.1", "2026-08-12T10:00:00Z", map[string]stageCount{
			"CONTRACT": {Pass: 1, Fail: 1},
		}),
		pvRow("linux", "", "node", "24.2", "2026-08-12T10:00:00Z", map[string]stageCount{
			"CONTRACT": {Fail: 3},
		}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	mixed := cellAt(t, g, "windows", "node 24")
	if mixed.Basis != "verified" || mixed.Ratio != "50%" || !mixed.Bang {
		t.Errorf("mixed cell = %q %q bang=%v, want verified 50%% with !",
			mixed.Basis, mixed.Ratio, mixed.Bang)
	}
	fail := cellAt(t, g, "linux", "node 24")
	if fail.Basis != "verified" || fail.Class != "verified" || fail.Ratio != "0%" || !fail.Bang {
		t.Errorf("fail cell = %q/%q %q bang=%v, want verified/verified 0%% with !",
			fail.Basis, fail.Class, fail.Ratio, fail.Bang)
	}
	if !g.HasBang {
		t.Error("grid must know it contains a ! marker for the legend")
	}
}

// Elevated failure on an observation row is also an anomaly.
func TestPivotElevatedObservationIsMarkedBang(t *testing.T) {
	r := pvRow("linux", "", "node", "20.9", "2026-08-12T10:00:00Z", map[string]stageCount{
		"PROJECT_LOAD": {Pass: 5, Fail: 7},
	})
	r.ElevatedFailure = true
	g := buildPivot([]snapshotRow{r}, osRowKey, contextColKey, nil, pivotNow)
	c := cellAt(t, g, "linux", "node 20")
	if !c.Bang {
		t.Error("elevated-failure observations must carry !")
	}
}

// Evidence older than the 90-day recency half-life is stale — marked "?"
// Age is not a defect here. A cell is one pinned release in one environment
// bucket, and neither moves: that axios 1.6.0 failed on node 20 does not
// become less true in ninety days. What can change is the environment, and a
// new environment is a different cell -- so the date is reported and nothing
// is marked stale.
func TestPivotDoesNotAgeAPinnedCoordinate(t *testing.T) {
	rows := []snapshotRow{
		pvRow("linux", "", "node", "22.1", "2026-05-01T00:00:00Z", map[string]stageCount{
			"CONTRACT": {Pass: 3},
		}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	c := cellAt(t, g, "linux", "node 22")
	if strings.Contains(c.Tip, "stale") {
		t.Errorf("tooltip %q still ages a pinned coordinate", c.Tip)
	}
	if !strings.Contains(c.Tip, "last seen 2026-05-01") {
		t.Errorf("tooltip %q must still date the evidence", c.Tip)
	}
	if c.Glyph != "◆" {
		t.Errorf("glyph = %q; old evidence is still evidence we ran", c.Glyph)
	}
}

func TestPivotCrossCheckedNeedsTwoPeers(t *testing.T) {
	one := pvRow("linux", "", "node", "22.1", "2026-08-12T10:00:00Z", map[string]stageCount{
		"CONTRACT": {Pass: 2},
	})
	one.VerificationCounts = map[string]int64{"distinctVerifyingPeers": 1}
	two := pvRow("windows", "", "node", "22.2", "2026-08-12T10:00:00Z", map[string]stageCount{
		"CONTRACT": {Pass: 2},
	})
	two.VerificationCounts = map[string]int64{"distinctVerifyingPeers": 2}
	g := buildPivot([]snapshotRow{one, two}, osRowKey, contextColKey, nil, pivotNow)
	if cellAt(t, g, "linux", "node 22").Cross {
		t.Error("one peer is not cross-checked")
	}
	c := cellAt(t, g, "windows", "node 22")
	if !c.Cross {
		t.Error("two distinct verifying peers is cross-checked")
	}
	if !strings.Contains(c.Tip, "cross-checked") {
		t.Errorf("tooltip %q does not say cross-checked", c.Tip)
	}
}

// Rows bucket by OS (+libc when it decides behaviour); columns bucket by
// runtime line + major version; darwin is displayed as macos.
func TestPivotAxisBucketing(t *testing.T) {
	rows := []snapshotRow{
		pvRow("linux", "", "node", "22.18", "2026-08-12T10:00:00Z", map[string]stageCount{"CONTRACT": {Pass: 1}}),
		pvRow("linux", "", "node", "22.4", "2026-08-12T10:00:00Z", map[string]stageCount{"CONTRACT": {Pass: 1}}),
		pvRow("linux", "musl", "node", "22.4", "2026-08-12T10:00:00Z", map[string]stageCount{"CONTRACT": {Fail: 1}}),
		pvRow("darwin", "", "node", "22.4", "2026-08-12T10:00:00Z", map[string]stageCount{"CONTRACT": {Pass: 1}}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	if len(g.Cols) != 1 || g.Cols[0].Label != "node 22" {
		t.Fatalf("cols = %v, want the two patch versions merged into [node 22]", g.Cols)
	}
	merged := cellAt(t, g, "linux", "node 22")
	if merged.Ver != 2 {
		t.Errorf("linux cell ver = %d, want the 22.18 and 22.4 rows merged to 2", merged.Ver)
	}
	if cellAt(t, g, "linux musl", "node 22").Ratio != "0%" {
		t.Error("musl is a separate row from glibc linux")
	}
	if cellAt(t, g, "macos", "node 22").Ratio != "100%" {
		t.Error("darwin must display as macos")
	}
}

// Columns group by line, newest major first; lines alphabetical. Rows keep
// the familiar linux, macos, windows order.
func TestPivotColumnAndRowOrder(t *testing.T) {
	mk := func(os, rt, ver string) snapshotRow {
		return pvRow(os, "", rt, ver, "2026-08-12T10:00:00Z", map[string]stageCount{"CONTRACT": {Pass: 1}})
	}
	rows := []snapshotRow{
		mk("windows", "node", "20.1"),
		mk("linux", "node", "24.0"),
		mk("darwin", "node", "22.5"),
		mk("linux", "bun", "1.2"),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	wantCols := []string{"bun 1", "node 24", "node 22", "node 20"}
	if colLabels(g) != strings.Join(wantCols, ",") {
		t.Errorf("cols = %v, want %v", g.Cols, wantCols)
	}
	var gotRows []string
	for _, r := range g.Rows {
		gotRows = append(gotRows, r.Label)
	}
	want := []string{"linux", "macos", "windows"}
	if strings.Join(gotRows, ",") != strings.Join(want, ",") {
		t.Errorf("rows = %v, want %v", gotRows, want)
	}
}

// A row whose environment never recorded an OS contributes to no pivot row:
// the pivot never guesses a dimension.
func TestPivotSkipsRowsWithoutTheAxis(t *testing.T) {
	rows := []snapshotRow{
		pvRow("", "", "node", "22.1", "2026-08-12T10:00:00Z", map[string]stageCount{"CONTRACT": {Pass: 9}}),
		pvRow("linux", "", "node", "22.1", "2026-08-12T10:00:00Z", map[string]stageCount{"CONTRACT": {Pass: 1}}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	if len(g.Rows) != 1 || g.Rows[0].Label != "linux" {
		t.Fatalf("rows = %+v, want only linux", g.Rows)
	}
	if got := cellAt(t, g, "linux", "node 22").Ver; got != 1 {
		t.Errorf("linux ver = %d — the OS-less row must not leak into it", got)
	}
}

// "linux" is not an answer to "does it run there": the row says which
// distribution and libc actually ran, because that is the difference
// between a native module loading and not.
func TestOSLabelNamesTheDistribution(t *testing.T) {
	cases := []struct {
		env  domain.EnvironmentFingerprint
		want string
	}{
		// The shape production actually records: the distribution arrives
		// in osVersionBucket, with distro empty.
		{domain.EnvironmentFingerprint{OS: "linux", OSVersionBucket: "alpine", Libc: "musl"}, "alpine musl"},
		{domain.EnvironmentFingerprint{OS: "linux", OSVersionBucket: "debian", Libc: "glibc"}, "debian glibc"},
		{domain.EnvironmentFingerprint{OS: "linux", Distro: "ubuntu", OSVersionBucket: "24.04", Libc: "glibc"}, "ubuntu glibc"},
		// A numeric bucket is a release of the OS, not a replacement for it.
		{domain.EnvironmentFingerprint{OS: "windows", OSVersionBucket: "11"}, "windows 11"},
		{domain.EnvironmentFingerprint{OS: "darwin"}, "macos"},
		{domain.EnvironmentFingerprint{OS: "linux"}, "linux"},
		{domain.EnvironmentFingerprint{}, ""},
	}
	for _, c := range cases {
		if got := osLabel(c.env); got != c.want {
			t.Errorf("osLabel(%+v) = %q, want %q", c.env, got, c.want)
		}
	}
}

// Distribution rows still sort with the Linux family first.
func TestPivotRowOrderKeepsLinuxFamilyFirst(t *testing.T) {
	got := sortPivotRows([]string{"windows 11", "macos", "debian glibc", "alpine musl"})
	want := "alpine musl,debian glibc,macos,windows 11"
	if strings.Join(got, ",") != want {
		t.Errorf("rows = %v, want %s", got, want)
	}
}

// Cells link where the caller says; empty cells never link.
func TestPivotCellHref(t *testing.T) {
	rows := []snapshotRow{
		pvRow("linux", "", "node", "22.1", "2026-08-12T10:00:00Z", map[string]stageCount{"CONTRACT": {Pass: 1}}),
		pvRow("windows", "", "node", "20.1", "2026-08-12T10:00:00Z", map[string]stageCount{"CONTRACT": {Pass: 1}}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, func(row, col string) string {
		return "/npm/axios/1.12.0"
	}, pivotNow)
	if got := cellAt(t, g, "linux", "node 22").Href; got != "/npm/axios/1.12.0" {
		t.Errorf("linked cell href = %q", got)
	}
	if got := cellAt(t, g, "linux", "node 20").Href; got != "" {
		t.Errorf("empty cell must not link, got %q", got)
	}
}

// A cell that folds many events says how many: "15/18", never a bare PASS
// pretending the slice is uniform.
func TestPivotCellRatioShowsHiddenDepth(t *testing.T) {
	rows := []snapshotRow{
		pvRow("linux", "", "node", "22.1", "2026-08-12T10:00:00Z", map[string]stageCount{
			"CONTRACT": {Pass: 15, Fail: 3},
		}),
		pvRow("windows", "", "node", "22.1", "2026-08-12T10:00:00Z", map[string]stageCount{
			"CONTRACT": {Pass: 1},
		}),
		pvRow("macos", "", "node", "22.1", "2026-08-12T10:00:00Z", map[string]stageCount{
			"PROJECT_COMPILE": {Pass: 7, Fail: 1},
		}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	if got := cellAt(t, g, "linux", "node 22").Ratio; got != "83%" {
		t.Errorf("mixed cell ratio = %q, want 83%%", got)
	}
	// A single run states its rate too, and the run count beside it is what
	// keeps 100%-of-one from reading like 100%-of-eighteen.
	single := cellAt(t, g, "windows", "node 22")
	if single.Ratio != "100%" || single.Runs != 1 {
		t.Errorf("single-event cell = %q of %d, want 100%% of 1", single.Ratio, single.Runs)
	}
	if deep := cellAt(t, g, "linux", "node 22"); deep.Runs != 18 {
		t.Errorf("mixed cell runs = %d, want the 18 the percentage rests on", deep.Runs)
	}
	if got := cellAt(t, g, "macos", "node 22").Ratio; got != "88%" {
		t.Errorf("observation-only cell ratio = %q, want 88%%", got)
	}
}

// More lines than fit keep the highest-evidence columns and say so.
func TestPivotCapsColumnsToHighestEvidence(t *testing.T) {
	var rows []snapshotRow
	lines := []string{"node", "bun", "deno", "python", "go", "rustc", "ruby", "php", "java", "dart"}
	for i, line := range lines {
		rows = append(rows, pvRow("linux", "", line, "9.0", "2026-08-12T10:00:00Z",
			map[string]stageCount{"CONTRACT": {Pass: int64(1 + i)}}))
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	if len(g.Cols) != pivotMaxCols {
		t.Fatalf("cols = %d, want capped at %d", len(g.Cols), pivotMaxCols)
	}
	if !g.Trimmed {
		t.Error("a capped grid must say it is trimmed")
	}
	for _, c := range g.Cols {
		if c.Label == "node 9" {
			t.Error("the lowest-evidence line survived the cap")
		}
	}
}
