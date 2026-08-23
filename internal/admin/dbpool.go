package admin

// The connection pool, as the operator screen shows it.
//
// R2C-55 was diagnosed by opening a database session on the production host
// and reading pg_stat_activity, because nothing this server exposed could say
// who was holding its eight connections. That is the gap this panel closes:
// how much of the pool is in use, which class of work is holding it, how long
// anyone had to wait for one, and how much was refused or cancelled -- all of
// it from counters the pool already keeps, so reading the panel costs no
// query and cannot itself become part of the incident.

import (
	"fmt"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// PoolStatsReader is the narrow seam onto the store's own connection pool.
// A store that cannot answer (the fake, a store without a pool) leaves the
// panel out rather than showing zeros.
type PoolStatsReader interface {
	PoolStats() serverstore.PoolStats
}

// poolClassView is one class's row.
type poolClassView struct {
	Label    string
	InUse    string
	Acquired uint64
	Waited   uint64
	WaitMax  string
	Busy     uint64
	Timeouts uint64
	// Strained is true when this class has been refused a connection or had
	// a statement cancelled. It drives the highlight, so the row that needs
	// attention is the one an operator's eye lands on.
	Strained bool
}

// poolView is the whole panel.
type poolView struct {
	Available bool
	// GuardOff records that the class ceilings are switched off
	// (CSX_DB_POOL_GUARD=off). It is stated rather than implied: a panel
	// showing no timeouts because nothing can time out reads exactly like a
	// healthy one.
	GuardOff bool
	InUse    int
	MaxConns int
	Idle     int
	Classes  []poolClassView
	Strained bool
}

var poolClassLabels = map[string]string{
	"interactive": "사용자 대기 읽기",
	"background":  "수집·집계",
	"probe":       "헬스 체크",
}

func buildPoolView(stats serverstore.PoolStats) poolView {
	v := poolView{
		Available: true,
		GuardOff:  !stats.Enabled,
		InUse:     stats.InUse,
		MaxConns:  stats.MaxConns,
		Idle:      stats.Idle,
	}
	for _, c := range stats.Classes {
		label := poolClassLabels[c.Class]
		if label == "" {
			label = c.Class
		}
		strained := c.Busy > 0 || c.Timeouts > 0
		v.Strained = v.Strained || strained
		v.Classes = append(v.Classes, poolClassView{
			Label:    label,
			InUse:    fmt.Sprintf("%d / %d", c.InUse, c.Limit),
			Acquired: c.Acquired,
			Waited:   c.Waited,
			WaitMax:  formatPoolWait(c.WaitMax),
			Busy:     c.Busy,
			Timeouts: c.Timeouts,
			Strained: strained,
		})
	}
	return v
}

// formatPoolWait reports the longest anyone queued. Zero is written as a
// dash and not as "0 ms": a class that never waited did not wait quickly,
// it did not wait.
func formatPoolWait(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	return formatLatency(d)
}
