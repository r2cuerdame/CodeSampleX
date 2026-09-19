package httpapi

// The process's own boot record, appended to GET /v1/ops/pool-metrics
// (#250).
//
// Production degradation is phase-dependent: warm traffic is fast, a
// restart is not. The v0.1.147 observer's 777% peak CPU, 143 pool refusals
// and 16.357 s maximum wait were all measured inside one restart window,
// and nothing an operator could poll said which of the five things
// csx-server does at boot -- the reconciles, the dedup purge, the Builder's
// first pass, the mux prewarm, and serving -- were running on top of each
// other at the time. cmd/csx-server/bootschedule.go now runs the
// maintenance steps one at a time under an explicit budget and starts the
// Builder only after they are done; this section is that schedule's
// receipt, so "did the restart overlap harmfully" is a poll, not a log
// archaeology.

import "time"

// BootTimeline is one process's boot as its schedule recorded it.
type BootTimeline struct {
	// StartedAt is process start (exec), the zero of every offset below.
	StartedAt time.Time
	// Marks are the instants between phases, in the order they happened:
	// migrated, wanted-primed, listen, maintenance-done, builder-started
	// (or builder-standalone when cmd/csx-builder owns the pipeline).
	Marks []BootMark
	// Phases are the maintenance steps, in the order they ran.
	Phases []BootPhase
	// MaintenanceDone is true once every step has run or been skipped for
	// budget. Until then the lane is still holding the Builder back.
	MaintenanceDone bool
}

// BootMark is one instant on the timeline.
type BootMark struct {
	Name      string
	AtSeconds float64
}

// BootPhase is the record of one maintenance step.
type BootPhase struct {
	Name             string
	BudgetSeconds    float64
	StartedAtSeconds float64
	Seconds          float64
	// Outcome is ok, failed, budget-exceeded or shutdown.
	Outcome string
	Detail  string
	// The pool's account of the step, read from the step's own query
	// budget: refusals, statements cancelled on a ceiling, time in line.
	PoolBusy        int64
	QueryTimeouts   int64
	PoolWaitSeconds float64
	// Concurrent names the lanes that were live when the step started
	// ("serving", "builder"). The schedule's invariant is that "builder"
	// never appears here.
	Concurrent []string
}

// BootTimelineSource is the narrow seam onto the schedule's record.
type BootTimelineSource interface {
	BootTimeline() BootTimeline
}

// opsBoot is BootTimeline on the wire. Measured is false when no source was
// wired (a test mux), following the routes/host rule that an unmeasured
// zero must not read as a clean boot.
type opsBoot struct {
	Measured        bool           `json:"measured"`
	StartedAt       string         `json:"startedAt,omitempty"`
	UptimeSeconds   float64        `json:"uptimeSeconds"`
	MaintenanceDone bool           `json:"maintenanceDone"`
	Marks           []opsBootMark  `json:"marks"`
	Phases          []opsBootPhase `json:"phases"`
}

type opsBootMark struct {
	Name      string  `json:"name"`
	AtSeconds float64 `json:"atSeconds"`
}

type opsBootPhase struct {
	Name             string   `json:"name"`
	BudgetSeconds    float64  `json:"budgetSeconds"`
	StartedAtSeconds float64  `json:"startedAtSeconds"`
	Seconds          float64  `json:"seconds"`
	Outcome          string   `json:"outcome"`
	Detail           string   `json:"detail,omitempty"`
	PoolBusy         int64    `json:"poolBusy"`
	QueryTimeouts    int64    `json:"queryTimeouts"`
	PoolWaitSeconds  float64  `json:"poolWaitSeconds"`
	Concurrent       []string `json:"concurrent"`
}

// opsBootFrom is the pure mapping; now is injected so the uptime is
// testable.
func opsBootFrom(tl BootTimeline, now time.Time) opsBoot {
	out := opsBoot{
		Measured:        true,
		StartedAt:       formatOpsTime(tl.StartedAt),
		MaintenanceDone: tl.MaintenanceDone,
		Marks:           make([]opsBootMark, 0, len(tl.Marks)),
		Phases:          make([]opsBootPhase, 0, len(tl.Phases)),
	}
	if !tl.StartedAt.IsZero() {
		out.UptimeSeconds = now.Sub(tl.StartedAt).Seconds()
	}
	for _, m := range tl.Marks {
		out.Marks = append(out.Marks, opsBootMark{Name: m.Name, AtSeconds: m.AtSeconds})
	}
	for _, p := range tl.Phases {
		concurrent := p.Concurrent
		if concurrent == nil {
			concurrent = []string{}
		}
		out.Phases = append(out.Phases, opsBootPhase{
			Name:             p.Name,
			BudgetSeconds:    p.BudgetSeconds,
			StartedAtSeconds: p.StartedAtSeconds,
			Seconds:          p.Seconds,
			Outcome:          p.Outcome,
			Detail:           p.Detail,
			PoolBusy:         p.PoolBusy,
			QueryTimeouts:    p.QueryTimeouts,
			PoolWaitSeconds:  p.PoolWaitSeconds,
			Concurrent:       concurrent,
		})
	}
	return out
}
