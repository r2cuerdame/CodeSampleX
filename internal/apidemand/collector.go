package apidemand

import (
	"context"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultFlushEvery = 30 * time.Second
	defaultQueueSize  = 4096
	defaultDBTimeout  = 5 * time.Second
	// DefaultRetention keeps five weeks: the fourteen days the report reads
	// plus enough slack that a late flush never trims the window it is
	// about to report on.
	DefaultRetention = 35 * 24 * time.Hour
	pruneEvery       = time.Hour
)

// Config tunes the collector. CountryHeader names the request header a
// trusted edge overwrites with the caller's country; empty disables the
// country dimension entirely rather than trusting a client-supplied value.
type Config struct {
	CountryHeader string
	FlushEvery    time.Duration
	QueueSize     int
	DBTimeout     time.Duration
	Retention     time.Duration
	Now           func() time.Time
}

// Telemetry is the collector's own health. A dropped or failed count means
// the dashboard is undercounting, and the dashboard says so.
type Telemetry struct {
	Pending       int64
	Dropped       uint64
	StoreFailures uint64
	Flushes       uint64
}

// Collector aggregates observations in memory and flushes them to the store
// on a cadence. The request path does one classification and one
// non-blocking send; a saturated queue drops the observation and counts
// the drop rather than delaying the response.
type Collector struct {
	store         Store
	countryHeader string
	now           func() time.Time
	flushEvery    time.Duration
	dbTimeout     time.Duration
	retention     time.Duration

	queue     chan Observation
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once

	pending       atomic.Int64
	dropped       atomic.Uint64
	storeFailures atomic.Uint64
	flushes       atomic.Uint64
}

// New starts a collector. A nil store yields a disabled collector whose
// Wrap is the identity and whose Available is false; the dashboard then
// shows "not measured" rather than zeros.
func New(ctx context.Context, store Store, cfg Config) *Collector {
	c := &Collector{
		store:         store,
		countryHeader: http.CanonicalHeaderKey(cfg.CountryHeader),
		now:           cfg.Now,
		flushEvery:    cfg.FlushEvery,
		dbTimeout:     cfg.DBTimeout,
		retention:     cfg.Retention,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.flushEvery <= 0 {
		c.flushEvery = defaultFlushEvery
	}
	if c.dbTimeout <= 0 {
		c.dbTimeout = defaultDBTimeout
	}
	if c.retention <= 0 {
		c.retention = DefaultRetention
	}
	if store == nil {
		close(c.done)
		return c
	}
	size := cfg.QueueSize
	if size <= 0 {
		size = defaultQueueSize
	}
	c.queue = make(chan Observation, size)
	go c.run()
	go func() {
		select {
		case <-ctx.Done():
			c.beginClose()
		case <-c.done:
		}
	}()
	return c
}

// Available reports whether observations are being recorded at all.
func (c *Collector) Available() bool { return c != nil && c.store != nil }

// CountryEnabled reports whether a trusted edge header is configured.
func (c *Collector) CountryEnabled() bool { return c != nil && c.countryHeader != "" }

// Telemetry returns the collector's own counters.
func (c *Collector) Telemetry() Telemetry {
	if c == nil {
		return Telemetry{}
	}
	return Telemetry{
		Pending:       c.pending.Load(),
		Dropped:       c.dropped.Load(),
		StoreFailures: c.storeFailures.Load(),
		Flushes:       c.flushes.Load(),
	}
}

// Report reads the bounded report from the store.
func (c *Collector) Report(ctx context.Context, now time.Time) (Report, error) {
	return c.store.DemandReport(ctx, now)
}

// Wrap records one observation per completed request on a fixed route
// label. The label must be the registered mux pattern; a value outside that
// grammar is not recorded at all, so a caller-controlled string can never
// become a dimension.
func (c *Collector) Wrap(route string, next http.Handler) http.Handler {
	if !c.Available() || !ValidRoute(route) {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := c.now()
		identity := Identify(r)
		client := ParseUserAgent(r.Header.Get("User-Agent"))
		country := ""
		if c.countryHeader != "" {
			country = Country(r.Header.Get(c.countryHeader))
		}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		latency := c.now().Sub(started)
		if latency < 0 {
			latency = 0
		}
		c.Observe(Observation{
			At:            started,
			Route:         route,
			Outcome:       Outcome(rec.status),
			Auth:          identity.Auth,
			CallerHash:    identity.CallerHash,
			Country:       country,
			ClientKind:    client.Kind,
			ClientVersion: client.Version,
			Protocol:      client.Protocol,
			LatencyMS:     latency.Milliseconds(),
		})
	})
}

// Observe enqueues one observation without blocking.
func (c *Collector) Observe(o Observation) {
	if !c.Available() {
		return
	}
	select {
	case c.queue <- o:
		c.pending.Add(1)
	default:
		c.dropped.Add(1)
	}
}

// Close flushes what is queued and stops the loop. It is safe to call more
// than once and after the context that started the collector ended.
func (c *Collector) Close(ctx context.Context) error {
	if c == nil || c.store == nil {
		return nil
	}
	c.beginClose()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Collector) beginClose() {
	c.closeOnce.Do(func() { close(c.stop) })
}

func (c *Collector) run() {
	defer close(c.done)
	batch := NewBatch()
	ticker := time.NewTicker(c.flushEvery)
	defer ticker.Stop()
	nextPrune := time.Time{}
	for {
		select {
		case o := <-c.queue:
			c.pending.Add(-1)
			batch.Add(o)
		case <-ticker.C:
			c.flush(&batch)
			if now := c.now(); !now.Before(nextPrune) {
				c.prune(now)
				nextPrune = now.Add(pruneEvery)
			}
		case <-c.stop:
			c.drain(&batch)
			c.flush(&batch)
			return
		}
	}
}

func (c *Collector) drain(batch *Batch) {
	for {
		select {
		case o := <-c.queue:
			c.pending.Add(-1)
			batch.Add(o)
		default:
			return
		}
	}
}

func (c *Collector) flush(batch *Batch) {
	if batch.Empty() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.dbTimeout)
	err := c.store.UpsertDemand(ctx, *batch)
	cancel()
	if err != nil {
		c.storeFailures.Add(1)
		// Fixed message: never a route, hash, country or count.
		log.Print("csx: api demand telemetry write unavailable (demand undercounted)")
	} else {
		c.flushes.Add(1)
	}
	// A failed batch is dropped rather than retried: retrying would double
	// count on a partial failure, and undercounting is the documented
	// failure mode.
	*batch = NewBatch()
}

func (c *Collector) prune(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), c.dbTimeout)
	defer cancel()
	if err := c.store.PruneDemand(ctx, now.Add(-c.retention)); err != nil {
		log.Print("csx: api demand telemetry prune unavailable (retention window may exceed policy)")
	}
}

// statusRecorder remembers the committed status; every other call passes
// through, and Unwrap keeps http.ResponseController working.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		flusher.Flush()
	}
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
