package compatibility

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const (
	phaseResume                    = "resume"
	phaseChanges                   = "changes"
	phaseListTargets               = "list_targets"
	phaseLoadSamples               = "load_samples"
	phaseSamplePageRead            = "sample_page_read"
	phaseReceiptPageRead           = "receipt_page_read"
	phaseDecode                    = "decode"
	phaseEnsureReceiptPackages     = "ensure_receipt_packages"
	phaseReceiptDerivedCalculation = "receipt_derived_calculation"
	phaseTargetEvidence            = "target_evidence"
	phaseSnapshotCalculate         = "snapshot_calculate"
	phaseSnapshotWrite             = "snapshot_write"
	phaseSnapshotRetire            = "snapshot_retire"
	phaseClusterRead               = "cluster_read"
	phaseClusterCalculate          = "cluster_calculate"
	phaseClusterWrite              = "cluster_write"
	phaseShards                    = "shards"
	phaseMatrixJobs                = "matrix_jobs"
	phaseMatrixJobHistoryRead      = "matrix_job_history_read"
	phaseDependencyAxis            = "dependency_axis"
	phaseRefreshStats              = "refresh_stats"
)

var builderPhaseNames = []string{
	phaseResume,
	phaseChanges,
	phaseListTargets,
	phaseLoadSamples,
	phaseSamplePageRead,
	phaseReceiptPageRead,
	phaseDecode,
	phaseEnsureReceiptPackages,
	phaseReceiptDerivedCalculation,
	phaseTargetEvidence,
	phaseSnapshotCalculate,
	phaseSnapshotWrite,
	phaseSnapshotRetire,
	phaseClusterRead,
	phaseClusterCalculate,
	phaseClusterWrite,
	phaseShards,
	phaseMatrixJobs,
	phaseMatrixJobHistoryRead,
	phaseDependencyAxis,
	phaseRefreshStats,
}

var builderProcessAttempt atomic.Uint64

func (b *Builder) newPhaseRecorder(ctx context.Context) *builderPhaseRecorder {
	now := b.phaseNow
	if now == nil {
		now = time.Now
	}
	logf := b.phaseLogf
	if logf == nil {
		logf = log.Printf
	}
	return newBuilderPhaseRecorder(ctx, now, logf)
}

type builderPhaseCounters struct {
	logicalCalls int64
	pages        int64
	items        int64
	bytes        int64
	callsKnown   bool
}

type builderPressure struct {
	busy, timeouts int64
	waited         time.Duration
	known          bool
}

type builderPhaseState struct {
	entered, closed bool
	failed          bool
	elapsed         time.Duration
	inFlight        int
	openStarted     time.Time
	counters        builderPhaseCounters
	pressure        builderPressure
}

type builderPhaseRecorder struct {
	ctx          context.Context
	attempt      uint64
	now          func() time.Time
	logf         func(string, ...any)
	started      time.Time
	lastProgress time.Time
	active       string
	failed       string
	states       map[string]*builderPhaseState
}

type builderPhaseToken struct {
	recorder *builderPhaseRecorder
	name     string
	started  time.Time
	pressure builderPressure
}

type builderPhaseContextKey struct{}

func newBuilderPhaseRecorder(ctx context.Context, now func() time.Time, logf func(string, ...any)) *builderPhaseRecorder {
	started := now()
	return &builderPhaseRecorder{
		ctx: ctx, attempt: builderProcessAttempt.Add(1), now: now, logf: logf,
		started: started, lastProgress: started, states: make(map[string]*builderPhaseState),
	}
}

func withBuilderPhaseRecorder(ctx context.Context, recorder *builderPhaseRecorder) context.Context {
	return context.WithValue(ctx, builderPhaseContextKey{}, recorder)
}

func builderPhases(ctx context.Context) *builderPhaseRecorder {
	recorder, _ := ctx.Value(builderPhaseContextKey{}).(*builderPhaseRecorder)
	if recorder == nil {
		now := time.Now
		started := now()
		return &builderPhaseRecorder{
			ctx: ctx, now: now, logf: func(string, ...any) {},
			started: started, lastProgress: started, states: make(map[string]*builderPhaseState),
		}
	}
	return recorder
}

func (r *builderPhaseRecorder) begin(name string) builderPhaseToken {
	state := r.states[name]
	if state == nil {
		state = &builderPhaseState{}
		r.states[name] = state
	}
	if !state.entered {
		state.entered = true
		r.logf("compatibility: phase attempt=%d event=enter phase=%s", r.attempt, name)
	}
	r.active = name
	started := r.now()
	if state.inFlight == 0 {
		state.openStarted = started
	}
	state.inFlight++
	return builderPhaseToken{recorder: r, name: name, started: started, pressure: builderPressureOf(r.ctx)}
}

func (token builderPhaseToken) end(err error, counters builderPhaseCounters) {
	r := token.recorder
	state := r.states[token.name]
	elapsed := r.now().Sub(token.started)
	if elapsed < 0 {
		elapsed = 0
	}
	state.elapsed += elapsed
	state.inFlight--
	state.counters.logicalCalls += counters.logicalCalls
	state.counters.pages += counters.pages
	state.counters.items += counters.items
	state.counters.bytes += counters.bytes
	state.counters.callsKnown = state.counters.callsKnown || counters.callsKnown
	state.pressure.addDelta(token.pressure, builderPressureOf(r.ctx))
	if err != nil {
		state.failed = true
		if r.failed == "" {
			r.failed = token.name
		}
		r.active = r.failed
	} else if r.active == token.name {
		r.active = ""
	}
}

func (r *builderPhaseRecorder) add(name string, counters builderPhaseCounters) {
	state := r.states[name]
	if state == nil {
		return
	}
	state.counters.logicalCalls += counters.logicalCalls
	state.counters.pages += counters.pages
	state.counters.items += counters.items
	state.counters.bytes += counters.bytes
	state.counters.callsKnown = state.counters.callsKnown || counters.callsKnown
}

func (r *builderPhaseRecorder) completeEmpty(name string) {
	if r.states[name] != nil {
		return
	}
	token := r.begin(name)
	token.end(nil, knownCalls(0))
}

func (r *builderPhaseRecorder) close(name string) {
	state := r.states[name]
	if state == nil || state.closed {
		return
	}
	state.closed = true
	outcome := "success"
	if state.failed {
		outcome = "error"
	}
	r.logf("compatibility: phase attempt=%d event=exit phase=%s outcome=%s elapsed_ms=%d logical_calls=%s pages=%d items=%d bytes_in_memory=%d pool_busy=%s query_timeouts=%s pool_wait_ms=%s db_acquisitions=unknown db_bytes=unknown",
		r.attempt, name, outcome, durationMillis(state.elapsed), callsValue(state.counters),
		state.counters.pages, state.counters.items, state.counters.bytes,
		pressureValue(state.pressure, state.pressure.busy),
		pressureValue(state.pressure, state.pressure.timeouts),
		pressureDurationValue(state.pressure))
}

func (r *builderPhaseRecorder) progress(name string) {
	now := r.now()
	if now.Sub(r.lastProgress) < 30*time.Second {
		return
	}
	state := r.states[name]
	if state == nil || state.closed {
		return
	}
	r.lastProgress = now
	elapsed := state.elapsed
	if state.inFlight > 0 {
		elapsed += now.Sub(state.openStarted)
	}
	r.logf("compatibility: phase attempt=%d event=progress phase=%s elapsed_ms=%d logical_calls=%s pages=%d items=%d bytes_in_memory=%d",
		r.attempt, name, durationMillis(elapsed), callsValue(state.counters), state.counters.pages,
		state.counters.items, state.counters.bytes)
}

func (r *builderPhaseRecorder) finish(runErr error) {
	for _, name := range builderPhaseNames {
		r.close(name)
	}
	class := builderPhaseErrorClass(runErr)
	outcome := "success"
	if class == "canceled" {
		outcome = "canceled"
	} else if class != "none" {
		outcome = "error"
	}
	active, failed := r.active, r.failed
	if active == "" {
		active = "none"
	}
	if failed == "" {
		failed = "none"
	}
	r.logf("compatibility: phase attempt=%d event=final outcome=%s error_class=%s active_phase=%s failed_phase=%s elapsed_ms=%d phase_elapsed_ms=%s",
		r.attempt, outcome, class, active, failed, durationMillis(r.now().Sub(r.started)), r.durationSummary())
}

func (r *builderPhaseRecorder) durationSummary() string {
	parts := make([]string, 0, len(r.states))
	for _, name := range builderPhaseNames {
		if state := r.states[name]; state != nil && state.entered {
			parts = append(parts, fmt.Sprintf("%s:%d", name, durationMillis(state.elapsed)))
		}
	}
	return strings.Join(parts, ",")
}

func builderPressureOf(ctx context.Context) builderPressure {
	budget := serverstore.BudgetOf(ctx)
	if budget == nil {
		return builderPressure{}
	}
	busy, timeouts, waited := budget.Pressure()
	return builderPressure{busy: busy, timeouts: timeouts, waited: waited, known: true}
}

func (p *builderPressure) addDelta(before, after builderPressure) {
	if !before.known || !after.known {
		return
	}
	p.known = true
	p.busy += after.busy - before.busy
	p.timeouts += after.timeouts - before.timeouts
	p.waited += after.waited - before.waited
}

func builderPhaseErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "error"
	}
}

func durationMillis(duration time.Duration) int64 {
	if duration < 0 {
		return 0
	}
	return duration.Milliseconds()
}

func callsValue(counters builderPhaseCounters) string {
	if !counters.callsKnown {
		return "unknown"
	}
	return fmt.Sprint(counters.logicalCalls)
}

func pressureValue(pressure builderPressure, value int64) string {
	if !pressure.known {
		return "unknown"
	}
	return fmt.Sprint(value)
}

func pressureDurationValue(pressure builderPressure) string {
	if !pressure.known {
		return "unknown"
	}
	return fmt.Sprint(durationMillis(pressure.waited))
}

func knownCalls(n int64) builderPhaseCounters {
	return builderPhaseCounters{logicalCalls: n, callsKnown: true}
}

func snapshotRowsBytes(rows []serverstore.SnapshotRow) int64 {
	var total int64
	for _, row := range rows {
		total += int64(len(row.SnapshotJSON))
	}
	return total
}

func sampleRowsBytes(rows []serverstore.SampleRow) int64 {
	var total int64
	for _, row := range rows {
		total += int64(len(row.ManifestJSON))
	}
	return total
}

func receiptRowsMetrics(rows []serverstore.ReceiptRow) (items, bytes int64) {
	items = int64(len(rows))
	for _, row := range rows {
		bytes += int64(len(row.ReceiptJSON))
	}
	return items, bytes
}

func receiptPagesMetrics(pages map[string][]serverstore.ReceiptRow) (items, bytes int64) {
	for _, rows := range pages {
		rowItems, rowBytes := receiptRowsMetrics(rows)
		items += rowItems
		bytes += rowBytes
	}
	return items, bytes
}

func jobRowsMetrics(rows []serverstore.JobRow) (items, bytes int64) {
	items = int64(len(rows))
	for _, row := range rows {
		bytes += int64(len(row.WantEnvJSON))
	}
	return items, bytes
}

func jobPagesMetrics(pages map[string][]serverstore.JobRow) (items, bytes int64) {
	for _, rows := range pages {
		rowItems, rowBytes := jobRowsMetrics(rows)
		items += rowItems
		bytes += rowBytes
	}
	return items, bytes
}
