package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func poolStatsFixture() serverstore.PoolStats {
	return serverstore.PoolStats{
		Enabled:  true,
		MaxConns: 8,
		Open:     8,
		InUse:    7,
		Idle:     1,
		Classes: []serverstore.ClassPoolStats{
			{Class: "interactive", Limit: 6, InUse: 6, Acquired: 4210, Waited: 118,
				WaitTotal: 90 * time.Second, WaitMax: 2900 * time.Millisecond, Busy: 12, Timeouts: 4},
			{Class: "background", Limit: 5, InUse: 1, Acquired: 320},
			{Class: "probe", Limit: 8, InUse: 0, Acquired: 60},
		},
	}
}

func TestPoolViewNamesWhoIsHoldingThePool(t *testing.T) {
	v := buildPoolView(poolStatsFixture())
	if !v.Available || v.GuardOff {
		t.Fatalf("the panel reported itself unavailable or unguarded: %+v", v)
	}
	if v.InUse != 7 || v.MaxConns != 8 {
		t.Fatalf("occupancy is wrong: %d / %d", v.InUse, v.MaxConns)
	}
	if !v.Strained {
		t.Fatal("a pool that refused twelve requests is not reported as strained")
	}
	if len(v.Classes) != 3 {
		t.Fatalf("got %d class rows", len(v.Classes))
	}
	read := v.Classes[0]
	if read.InUse != "6 / 6" {
		t.Errorf("the read class row does not show it is at its ceiling: %q", read.InUse)
	}
	if !read.Strained || read.Busy != 12 || read.Timeouts != 4 {
		t.Errorf("the strained row lost its numbers: %+v", read)
	}
	if read.WaitMax != "2900 ms" {
		t.Errorf("the longest wait is not readable: %q", read.WaitMax)
	}
	// A quiet class must not be highlighted, or everything is highlighted
	// and nothing is.
	if v.Classes[1].Strained || v.Classes[2].Strained {
		t.Error("a class with no refusals was marked strained")
	}
	// A class that never waited did not wait quickly.
	if v.Classes[2].WaitMax != "—" {
		t.Errorf("a class that never queued reports %q", v.Classes[2].WaitMax)
	}
}

// A panel showing no timeouts because nothing can time out reads exactly
// like a healthy one, so the disabled guard has to say so.
func TestPoolViewSaysWhenTheGuardIsOff(t *testing.T) {
	stats := poolStatsFixture()
	stats.Enabled = false
	if v := buildPoolView(stats); !v.GuardOff {
		t.Fatal("a disabled guard is not reported")
	}
}

func TestDashboardRendersThePoolPanel(t *testing.T) {
	data := dashboardData{
		Version:     "test",
		GeneratedAt: "2026-08-23T00:00:00Z",
		DBPool:      buildPoolView(poolStatsFixture()),
	}
	var body strings.Builder
	if err := dashboardTemplate.Execute(&body, data); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := body.String()
	for _, want := range []string{
		"데이터베이스 커넥션 풀",
		"사용자 대기 읽기",
		"6 / 6",
		"2900 ms",
		"docs/operations.md",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the rendered dashboard does not contain %q", want)
		}
	}
}

// A store with no pool leaves the panel out. Zeros would read as a healthy
// pool rather than as no pool at all.
func TestDashboardOmitsThePoolPanelWithoutAPool(t *testing.T) {
	var body strings.Builder
	if err := dashboardTemplate.Execute(&body, dashboardData{Version: "test"}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(body.String(), "데이터베이스 커넥션 풀") {
		t.Error("the pool panel rendered for a store that has no pool")
	}
}
