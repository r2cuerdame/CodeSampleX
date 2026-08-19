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

func cellAt(t *testing.T, g pivotGrid, row, col string) pivotCell {
	t.Helper()
	ci := -1
	for i, c := range g.Cols {
		if c == col {
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
	if c.State != "PASS" || c.Class != "pass" {
		t.Errorf("state = %q/%q, want verified PASS", c.State, c.Class)
	}
	if c.Bang || c.Maybe {
		t.Errorf("clean verified pass carries markers: bang=%v maybe=%v", c.Bang, c.Maybe)
	}
}

// A cell with only build observations is honest about its weakness: it
// renders OBSERVED with the "?" marker, never PASS.
func TestPivotObservationOnlyCellIsMarkedUncertain(t *testing.T) {
	rows := []snapshotRow{
		pvRow("linux", "", "node", "22.18", "2026-08-12T10:00:00Z", map[string]stageCount{
			"PROJECT_COMPILE": {Pass: 40},
		}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	c := cellAt(t, g, "linux", "node 22")
	if c.State != "OBSERVED" || c.Class != "observed" {
		t.Fatalf("state = %q/%q, want OBSERVED/observed", c.State, c.Class)
	}
	if !c.Maybe {
		t.Error("observation-only cell must carry the ? marker")
	}
	if c.Bang {
		t.Error("passing observations are not an anomaly")
	}
	if !g.HasMaybe {
		t.Error("grid must know it contains a ? marker for the legend")
	}
}

// A verification failure is a measured anomaly: "!" appears and the state
// says FAIL (or MIXED when passes exist beside it).
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
	if mixed.State != "MIXED" || !mixed.Bang {
		t.Errorf("mixed cell = %q bang=%v, want MIXED with !", mixed.State, mixed.Bang)
	}
	fail := cellAt(t, g, "linux", "node 24")
	if fail.State != "FAIL" || fail.Class != "fail" || !fail.Bang {
		t.Errorf("fail cell = %q/%q bang=%v, want FAIL/fail with !", fail.State, fail.Class, fail.Bang)
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
// and named stale in the tooltip.
func TestPivotStaleCellIsMarkedMaybe(t *testing.T) {
	rows := []snapshotRow{
		pvRow("linux", "", "node", "22.1", "2026-05-01T00:00:00Z", map[string]stageCount{
			"CONTRACT": {Pass: 3},
		}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	c := cellAt(t, g, "linux", "node 22")
	if !c.Stale || !c.Maybe {
		t.Fatalf("stale=%v maybe=%v, want both true for 110-day-old evidence", c.Stale, c.Maybe)
	}
	if !strings.Contains(c.Tip, "stale") {
		t.Errorf("tooltip %q does not say stale", c.Tip)
	}
}

// A fresh build observation must not freshen a stale verification: the
// cell's verdict comes from the verification side, so its staleness does
// too.
func TestPivotStaleFollowsVerificationRecency(t *testing.T) {
	rows := []snapshotRow{
		// 230-day-old contract pass decides the state…
		pvRow("linux", "", "node", "22.14", "2026-01-01T00:00:00Z", map[string]stageCount{
			"CONTRACT": {Pass: 1},
		}),
		// …while a fresh observation arrives in the same bucket.
		pvRow("linux", "", "node", "22.18", "2026-08-18T00:00:00Z", map[string]stageCount{
			"PROJECT_COMPILE": {Pass: 1},
		}),
	}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	c := cellAt(t, g, "linux", "node 22")
	if c.State != "PASS" {
		t.Fatalf("state = %q, want PASS from the verification", c.State)
	}
	if !c.Stale || !c.Maybe {
		t.Errorf("stale=%v maybe=%v — the fresh observation hid the verification's age", c.Stale, c.Maybe)
	}
	if !strings.Contains(c.Tip, "last seen 2026-01-01") {
		t.Errorf("tooltip %q must date the evidence the verdict rests on", c.Tip)
	}
}

// Cross-checked needs at least two distinct verifying peers.
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
	if len(g.Cols) != 1 || g.Cols[0] != "node 22" {
		t.Fatalf("cols = %v, want the two patch versions merged into [node 22]", g.Cols)
	}
	merged := cellAt(t, g, "linux", "node 22")
	if merged.Ver != 2 {
		t.Errorf("linux cell ver = %d, want the 22.18 and 22.4 rows merged to 2", merged.Ver)
	}
	if cellAt(t, g, "linux musl", "node 22").State != "FAIL" {
		t.Error("musl is a separate row from glibc linux")
	}
	if cellAt(t, g, "macos", "node 22").State != "PASS" {
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
	if strings.Join(g.Cols, ",") != strings.Join(wantCols, ",") {
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
	if got := cellAt(t, g, "linux", "node 22").Ratio; got != "15/18" {
		t.Errorf("mixed cell ratio = %q, want 15/18", got)
	}
	if got := cellAt(t, g, "windows", "node 22").Ratio; got != "" {
		t.Errorf("single-event cell ratio = %q, want empty", got)
	}
	if got := cellAt(t, g, "macos", "node 22").Ratio; got != "7/8" {
		t.Errorf("observation-only cell ratio = %q, want 7/8", got)
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
		if c == "node 9" {
			t.Error("the lowest-evidence line survived the cap")
		}
	}
}
