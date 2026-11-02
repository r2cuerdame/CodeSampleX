package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type statsDiagnosticKey struct{}
type statsDiagnostic struct {
	mu                   sync.Mutex
	w                    io.Writer
	id                   string
	started              time.Time
	events, bytes        int
	ageRead, receiptRead bool
}

var statsDiagnosticStages = map[string]bool{"command": true, "activation": true, "home": true, "daemon_probe": true, "config": true, "db_open": true, "cas_open": true, "hit_summary": true, "intervention": true, "counters": true, "delivery": true, "ledger": true, "cas_size": true, "packages": true, "queue": true, "report_age": true, "refused": true, "delivery_receipt": true, "diagnostic_open": true}

func WithStatsDiagnostics(ctx context.Context, w io.Writer) context.Context {
	if StatsDiagnosticsEnabled(ctx) || w == nil {
		return ctx
	}
	id := os.Getenv("CSX_STATS_TRACE_ID")
	if !domain.IsDiagnosticRequestID(id) {
		id = domain.NewDiagnosticRequestID()
	}
	if !domain.IsDiagnosticRequestID(id) {
		return ctx
	}
	d := &statsDiagnostic{w: w, id: id, started: time.Now()}
	ctx = context.WithValue(ctx, statsDiagnosticKey{}, d)
	d.write(fmt.Sprintf("csx_stats request_id=%s stage=command state=begin elapsed_ms=0\n", id))
	return ctx
}
func StatsDiagnosticsEnabled(ctx context.Context) bool {
	_, ok := ctx.Value(statsDiagnosticKey{}).(*statsDiagnostic)
	return ok
}
func statsElapsed(t time.Time) int64 {
	v := time.Since(t).Milliseconds()
	if v < 0 {
		return 0
	}
	if v > 86400000 {
		return 86400000
	}
	return v
}
func (d *statsDiagnostic) write(line string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.events >= 48 || d.bytes+len(line) > 8192 {
		return
	}
	d.events++
	d.bytes += len(line)
	_, _ = io.WriteString(d.w, line)
}
func BeginStatsStage(ctx context.Context, stage string) func(error) {
	d, ok := ctx.Value(statsDiagnosticKey{}).(*statsDiagnostic)
	if !ok || !statsDiagnosticStages[stage] {
		return func(error) {}
	}
	start := time.Now()
	d.write(fmt.Sprintf("csx_stats request_id=%s stage=%s state=begin elapsed_ms=0\n", d.id, stage))
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			state := "end"
			if err != nil {
				state = "failed"
			}
			d.write(fmt.Sprintf("csx_stats request_id=%s stage=%s state=%s elapsed_ms=%d\n", d.id, stage, state, statsElapsed(start)))
		})
	}
}
func FinishStatsDiagnostics(ctx context.Context, ok bool) {
	if d, found := ctx.Value(statsDiagnosticKey{}).(*statsDiagnostic); found {
		state := "end"
		if !ok {
			state = "failed"
		}
		d.write(fmt.Sprintf("csx_stats request_id=%s stage=command state=%s elapsed_ms=%d\n", d.id, state, statsElapsed(d.started)))
	}
}
func (d *Daemon) emitStatsQueueAge(ctx context.Context, uploads int) {
	trace, ok := ctx.Value(statsDiagnosticKey{}).(*statsDiagnostic)
	if !ok {
		return
	}
	trace.mu.Lock()
	if trace.ageRead {
		trace.mu.Unlock()
		return
	}
	trace.ageRead = true
	trace.mu.Unlock()
	coverage, age := "empty", "unknown"
	if uploads > 0 {
		end := BeginStatsStage(ctx, "report_age")
		readCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		oldest, count, valid, err := d.DB.PendingUploadOldest(readCtx, 1000)
		cancel()
		end(err)
		coverage = "unknown"
		seconds := int64(time.Since(oldest).Seconds())
		if err == nil && valid && count > 0 && seconds >= 0 && seconds <= 315360000 {
			coverage = "complete"
			if count == 1000 {
				coverage = "capped"
			}
			age = fmt.Sprint(seconds)
		}
	}
	trace.write(fmt.Sprintf("csx_stats_age request_id=%s report_oldest_seconds=%s coverage=%s evidence_oldest_seconds=unrecorded\n", trace.id, age, coverage))
}
func (d *Daemon) emitStatsDelivery(ctx context.Context) {
	trace, ok := ctx.Value(statsDiagnosticKey{}).(*statsDiagnostic)
	if !ok {
		return
	}
	trace.mu.Lock()
	if trace.receiptRead {
		trace.mu.Unlock()
		return
	}
	trace.receiptRead = true
	trace.mu.Unlock()
	end := BeginStatsStage(ctx, "delivery_receipt")
	readCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	values, readErr := d.DB.ReadDeliveryDiagnostics(readCtx)
	for _, item := range []struct{ key, kind string }{{statLastDeliveryAttempt, "attempt"}, {statLastDeliveryACK, "ack"}} {
		raw, found := values[item.key]
		if !found || len(raw) > 512 {
			continue
		}
		var v evidence.DeliveryDiagnostic
		if json.Unmarshal([]byte(raw), &v) != nil {
			continue
		}
		if !(domain.IsDiagnosticRequestID(v.AttemptID) || v.AttemptID == "unknown") || !(domain.IsDiagnosticRequestID(v.RequestID) || v.RequestID == "unknown") {
			continue
		}
		switch v.Outcome {
		case "ack", "ack_missing", "ack_invalid", "transport_error", "http_error", "unknown":
		default:
			continue
		}
		if v.HTTPStatus < 0 || v.HTTPStatus > 599 || v.Accepted < -1 || v.Accepted > 500 || v.Rejected < -1 || v.Rejected > 500 || v.ElapsedMillis < 0 || v.ElapsedMillis > 86400000 {
			continue
		}
		when, err := time.Parse(time.RFC3339, v.RecordedAt)
		if err != nil || when.UTC().Format(time.RFC3339) != v.RecordedAt {
			continue
		}
		trace.write(fmt.Sprintf("csx_delivery kind=%s attempt_id=%s request_id=%s outcome=%s http_status=%d accepted=%d rejected=%d elapsed_ms=%d recorded_at=%s\n", item.kind, v.AttemptID, v.RequestID, v.Outcome, v.HTTPStatus, v.Accepted, v.Rejected, v.ElapsedMillis, v.RecordedAt))
	}
	end(readErr)
}

// ReadStatsDiagnosticsFromDisk supplements a successful daemon fast path with
// only bounded diagnostic reads, never another full StatsNow/CAS walk.
func ReadStatsDiagnosticsFromDisk(ctx context.Context, home string, uploads int) {
	trace, ok := ctx.Value(statsDiagnosticKey{}).(*statsDiagnostic)
	if !ok {
		return
	}
	trace.mu.Lock()
	already := trace.ageRead && trace.receiptRead
	trace.mu.Unlock()
	if already {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	end := BeginStatsStage(ctx, "diagnostic_open")
	db, err := localdb.OpenReadOnly(readCtx, filepath.Join(home, "csx.db"))
	end(err)
	if err != nil {
		return
	}
	defer db.Close()
	d := &Daemon{DB: db}
	d.emitStatsQueueAge(readCtx, uploads)
	d.emitStatsDelivery(readCtx)
}
