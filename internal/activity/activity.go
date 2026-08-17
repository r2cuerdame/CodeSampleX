// Package activity estimates external API network activity with keyed,
// epoch-scoped pseudonyms instead of retaining raw request identifiers. The
// colocated HMAC key is the privacy boundary: anyone who compromises both the
// database and key can enumerate the IPv4 input space and recover the IPv4
// addresses represented by retained buckets. Raw addresses and user agents
// otherwise exist only for request classification and bucket derivation.
package activity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	KindDay   = "day"
	KindMonth = "month"

	// DailyWindowDays is the length of the daily chart. It is deliberately
	// shorter than the 35 retained daily epochs so the newest end of the
	// chart can never be truncated by a prune that runs mid-render.
	DailyWindowDays = 31

	defaultQueueSize   = 512
	defaultBatchSize   = 128
	defaultFlush       = 250 * time.Millisecond
	defaultDBTimeout   = 2 * time.Second
	defaultHealthRetry = 5 * time.Minute
	defaultMaintenance = 6 * time.Hour
	defaultSeenLimit   = 65536
)

var (
	ErrUnavailable = errors.New("activity estimates unavailable")
	ErrInvalidKey  = errors.New("activity hash key invalid")
	// ErrNoNetworkIdentity means the trust boundary refused to derive an
	// identity for this request — the fail-closed outcome, not a fault.
	ErrNoNetworkIdentity = errors.New("activity network identity not derivable")
)

// Bucket is the complete persistence boundary. It intentionally has no raw
// address, header, route, or stable identifier field.
type Bucket struct {
	Kind   string
	Epoch  string
	Value  [16]byte
	Owner  bool
	SeenAt time.Time
}

// Store is optional and deliberately separate from serverstore.Store.
type Store interface {
	RecordActivity(ctx context.Context, buckets []Bucket) error
	ActivityCounts(ctx context.Context, dayEpoch, monthEpoch string) (Counts, error)
	// ActivityDaily reports the retained daily epochs inside the inclusive
	// [fromEpoch, toEpoch] range, plus the oldest daily epoch still retained
	// anywhere in the table. The oldest epoch may predate fromEpoch, which is
	// exactly how a full chart is distinguished from one whose left edge is
	// simply older than collection itself.
	ActivityDaily(ctx context.Context, fromEpoch, toEpoch string) (DailyRaw, error)
}

// MaintenanceStore is deliberately narrower than Store. Server startup and
// periodic retention remain available even if collection or dashboard method
// wiring changes independently.
type MaintenanceStore interface {
	MarkActivityHealthy(ctx context.Context, now time.Time) error
	PruneActivity(ctx context.Context, now time.Time) error
}

type Counts struct {
	ExternalDAU int64
	ExternalMAU int64
	OwnerDAU    int64
	OwnerMAU    int64
	DaySeen     bool
	MonthSeen   bool
}

// DayCount is one stored daily epoch. Count excludes owner buckets; Rows
// includes them, so a day the operator alone touched is still provably
// collected rather than being reported as a gap.
type DayCount struct {
	Epoch         string
	Count         int64
	OwnerExcluded int64
	Rows          int64
	Healthy       bool
}

// DailyRaw is the unshaped store result behind the daily chart.
type DailyRaw struct {
	Days []DayCount
	// OldestEpoch is the oldest retained daily epoch, "" when none exists.
	OldestEpoch string
}

// DayPoint is one column of the daily chart. BeforeCollection marks a day
// older than anything retained — the chart cannot claim zero there, only
// silence. Gap marks a day inside the collected range that stored nothing,
// which is a real reporting gap and is shown as one.
type DayPoint struct {
	Epoch            string
	Count            int64
	BeforeCollection bool
	Gap              bool
	HealthyZero      bool
}

// DailyWindow is exactly DailyWindowDays points, oldest first.
type DailyWindow struct {
	Points     []DayPoint
	StartEpoch string
	Gaps       int
	Max        int64
	Collecting bool
}

// Telemetry is the collector's own health. Flushes counts batches persisted
// successfully; StoreFailures counts persistence errors; Dropped counts
// observations that never reached storage, whether the queue was saturated or
// a flush failed. Every one of them makes the reported estimate a floor.
type Telemetry struct {
	Dropped       uint64
	StoreFailures uint64
	Flushes       uint64
	Pending       int
}

type Metrics struct {
	Counts    Counts
	Daily     DailyWindow
	Telemetry Telemetry
}

// Config exposes small test seams while production uses bounded defaults.
type Config struct {
	HashKeyHex       string
	QueueSize        int
	BatchSize        int
	SeenLimit        int
	FlushEvery       time.Duration
	MaintenanceEvery time.Duration
	DBTimeout        time.Duration
	Now              func() time.Time
	newHealthTimer   func(time.Duration) activityTimer
}

type activityTimer interface {
	Ticks() <-chan time.Time
	Reset(time.Duration)
	Stop()
}

type systemTimer struct {
	timer *time.Timer
}

func (t *systemTimer) Ticks() <-chan time.Time { return t.timer.C }
func (t *systemTimer) Reset(d time.Duration)   { t.timer.Reset(d) }
func (t *systemTimer) Stop()                   { t.timer.Stop() }

type observation struct {
	day   Bucket
	month Bucket
}

type Tracker struct {
	store            Store
	maintenanceStore MaintenanceStore
	key              [sha256.Size]byte
	ready            bool
	badKey           bool
	now              func() time.Time
	queue            chan observation
	batchSize        int
	flush            time.Duration
	maintenance      time.Duration
	dbTimeout        time.Duration
	seenLimit        int
	running          bool
	seenMu           sync.Mutex
	seenDay          epochSeen
	seenMonth        epochSeen
	dropped          atomic.Uint64
	storeFailures    atomic.Uint64
	flushes          atomic.Uint64
	pending          atomic.Int64
	gate             sync.RWMutex
	accepting        bool
	stop             chan struct{}
	done             chan struct{}
	closeOnce        sync.Once
	handlers         sync.WaitGroup
	newHealthTimer   func(time.Duration) activityTimer
}

type epochSeen struct {
	epoch  string
	owners map[[16]byte]bool
}

// New validates a dedicated 256-bit key. Store maintenance starts whenever a
// store is available, independently of whether collection is configured, so
// retention and daily health markers never depend on possession of the key.
func New(ctx context.Context, store Store, cfg Config) *Tracker {
	maintenance, _ := store.(MaintenanceStore)
	return NewWithMaintenance(ctx, store, maintenance, cfg)
}

// NewWithMaintenance allows server wiring to retain maintenance even when the
// broader optional collection Store contract is unavailable.
func NewWithMaintenance(ctx context.Context, store Store, maintenanceStore MaintenanceStore, cfg Config) *Tracker {
	t := &Tracker{
		store: store, maintenanceStore: maintenanceStore, now: cfg.Now, batchSize: cfg.BatchSize, flush: cfg.FlushEvery, dbTimeout: cfg.DBTimeout,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	if t.now == nil {
		t.now = time.Now
	}
	t.newHealthTimer = cfg.newHealthTimer
	if t.newHealthTimer == nil {
		t.newHealthTimer = func(d time.Duration) activityTimer {
			return &systemTimer{timer: time.NewTimer(d)}
		}
	}
	if t.batchSize <= 0 {
		t.batchSize = defaultBatchSize
	}
	if t.flush <= 0 {
		t.flush = defaultFlush
	}
	if cfg.MaintenanceEvery <= 0 {
		t.maintenance = defaultMaintenance
	} else {
		t.maintenance = cfg.MaintenanceEvery
	}
	if t.dbTimeout <= 0 {
		t.dbTimeout = defaultDBTimeout
	}
	if cfg.SeenLimit <= 0 {
		t.seenLimit = defaultSeenLimit
	} else {
		t.seenLimit = cfg.SeenLimit
	}
	if cfg.HashKeyHex != "" {
		decoded, err := hex.DecodeString(cfg.HashKeyHex)
		if err != nil || len(decoded) != sha256.Size || len(cfg.HashKeyHex) != hex.EncodedLen(sha256.Size) {
			for i := range decoded {
				decoded[i] = 0
			}
			t.badKey = true
		} else {
			copy(t.key[:], decoded)
			for i := range decoded {
				decoded[i] = 0
			}
			if store != nil {
				queueSize := cfg.QueueSize
				if queueSize <= 0 {
					queueSize = defaultQueueSize
				}
				t.queue = make(chan observation, queueSize)
				t.ready = true
				t.accepting = true
			}
		}
	}
	if store == nil && maintenanceStore == nil {
		close(t.done)
		return t
	}
	t.running = true
	go t.run()
	go func() {
		select {
		case <-ctx.Done():
			t.beginClose()
		case <-t.done:
		}
	}()
	return t
}

func (t *Tracker) Available() bool            { return t != nil && t.ready }
func (t *Tracker) ConfigurationInvalid() bool { return t != nil && t.badKey }

// Wrap records only meaningful, non-bot public API traffic. Derivation and a
// nonblocking enqueue are the only request-path work; storage can never delay
// or fail the wrapped response.
func (t *Tracker) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracked := t.observe(r)
		if tracked {
			defer t.handlers.Done()
		}
		next.ServeHTTP(w, r)
	})
}

func (t *Tracker) observe(r *http.Request) bool {
	if t == nil || !t.ready {
		return false
	}
	t.gate.RLock()
	defer t.gate.RUnlock()
	if !t.accepting {
		if meaningfulRoute(r.Method, r.URL.EscapedPath()) && !likelyAutomated(r.UserAgent()) {
			t.dropped.Add(1)
		}
		return false
	}
	// Add while holding the admission lock: Close cannot begin waiting until
	// every request admitted before the cutoff has incremented the WaitGroup.
	t.handlers.Add(1)
	if !meaningfulRoute(r.Method, r.URL.EscapedPath()) || likelyAutomated(r.UserAgent()) {
		return true
	}
	obs, ok := t.observationFor(r, t.now(), false)
	if !ok {
		return true
	}
	t.pending.Add(1)
	select {
	case t.queue <- obs:
	default:
		t.pending.Add(-1)
		t.dropped.Add(1)
	}
	return true
}

// Close first closes the admission gate, then waits for the worker to drain
// both its staged batch and every observation that crossed the gate. A caller
// may retry Close with a fresh context after a timeout; Pending continues to
// include every item the worker has not yet persisted or recorded as dropped.
func (t *Tracker) Close(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.beginClose()
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Tracker) beginClose() {
	if t == nil || !t.running {
		return
	}
	t.closeOnce.Do(func() {
		if t.ready {
			t.gate.Lock()
			t.accepting = false
			t.gate.Unlock()
		}
		go func() {
			if t.ready {
				t.handlers.Wait()
			}
			close(t.stop)
		}()
	})
}

// MarkOwner synchronously upserts the authenticated admin request's current
// buckets. A later queued non-owner observation cannot undo owner=true.
func (t *Tracker) MarkOwner(ctx context.Context, r *http.Request, now time.Time) error {
	if err := t.availabilityError(); err != nil {
		return err
	}
	obs, ok := t.observationFor(r, now, true)
	if !ok {
		return ErrNoNetworkIdentity
	}
	if err := t.store.RecordActivity(ctx, []Bucket{obs.day, obs.month}); err != nil {
		t.storeFailures.Add(1)
		return err
	}
	t.remember([]Bucket{obs.day, obs.month})
	return nil
}

func (t *Tracker) Metrics(ctx context.Context, now time.Time) (Metrics, error) {
	if err := t.availabilityError(); err != nil {
		return Metrics{}, err
	}
	counts, err := t.store.ActivityCounts(ctx, dayEpoch(now), monthEpoch(now))
	if err != nil {
		return Metrics{Telemetry: t.telemetry()}, err
	}
	from, to := dailyWindowRange(now)
	raw, err := t.store.ActivityDaily(ctx, from, to)
	if err != nil {
		return Metrics{Counts: counts, Telemetry: t.telemetry()}, err
	}
	return Metrics{Counts: counts, Daily: BuildDailyWindow(now, raw), Telemetry: t.telemetry()}, nil
}

// dailyWindowRange is the inclusive day-epoch range of the chart, ending on
// the current UTC day.
func dailyWindowRange(now time.Time) (from, to string) {
	end := now.UTC()
	return dayEpoch(end.AddDate(0, 0, -(DailyWindowDays - 1))), dayEpoch(end)
}

// BuildDailyWindow shapes stored epochs into a fixed-length chart that never
// invents a zero. Days older than the oldest retained epoch are marked
// BeforeCollection rather than counted as zero activity, and days at or after
// it that stored nothing are marked as gaps.
func BuildDailyWindow(now time.Time, raw DailyRaw) DailyWindow {
	stored := make(map[string]DayCount, len(raw.Days))
	for _, d := range raw.Days {
		stored[d.Epoch] = d
	}
	window := DailyWindow{
		Points:     make([]DayPoint, 0, DailyWindowDays),
		StartEpoch: raw.OldestEpoch,
		Collecting: raw.OldestEpoch != "",
	}
	end := now.UTC()
	for i := DailyWindowDays - 1; i >= 0; i-- {
		epoch := dayEpoch(end.AddDate(0, 0, -i))
		point := DayPoint{Epoch: epoch}
		switch {
		case !window.Collecting || epoch < raw.OldestEpoch:
			// Epoch strings are fixed-width and zero-padded, so a lexical
			// comparison is a chronological one.
			point.BeforeCollection = true
		default:
			day, ok := stored[epoch]
			switch {
			case ok && day.Rows > 0:
				point.Count = day.Count
			case ok && day.Healthy:
				point.HealthyZero = true
			default:
				point.Gap = true
				window.Gaps++
			}
		}
		if point.Count > window.Max {
			window.Max = point.Count
		}
		window.Points = append(window.Points, point)
	}
	// A chart whose left edge is already inside collection reports the window
	// start, not a retention artifact older than anything it can draw.
	if window.Collecting && len(window.Points) > 0 && window.StartEpoch < window.Points[0].Epoch {
		window.StartEpoch = window.Points[0].Epoch
	}
	return window
}

func (t *Tracker) availabilityError() error {
	if t != nil && t.badKey {
		return ErrInvalidKey
	}
	if t == nil || !t.ready {
		return ErrUnavailable
	}
	return nil
}

func (t *Tracker) telemetry() Telemetry {
	pending := 0
	if t != nil {
		pending = int(t.pending.Load())
	}
	return Telemetry{Dropped: t.dropped.Load(), StoreFailures: t.storeFailures.Load(), Flushes: t.flushes.Load(), Pending: pending}
}

func (t *Tracker) Telemetry() Telemetry { return t.telemetry() }

func (t *Tracker) observationFor(r *http.Request, now time.Time, owner bool) (observation, bool) {
	addr, ok := requestAddress(r)
	if !ok {
		return observation{}, false
	}
	network := normalizedNetwork(addr)
	day, month := dayEpoch(now), monthEpoch(now)
	seen := now.UTC()
	return observation{
		day:   Bucket{Kind: KindDay, Epoch: day, Value: t.derive(KindDay, day, network), Owner: owner, SeenAt: seen},
		month: Bucket{Kind: KindMonth, Epoch: month, Value: t.derive(KindMonth, month, network), Owner: owner, SeenAt: seen},
	}, true
}

func (t *Tracker) derive(kind, epoch string, network []byte) [16]byte {
	mac := hmac.New(sha256.New, t.key[:])
	_, _ = mac.Write([]byte("codesamplex/activity/v1/" + kind + "\x00" + epoch + "\x00"))
	_, _ = mac.Write(network)
	sum := mac.Sum(nil)
	var out [16]byte
	copy(out[:], sum[:len(out)])
	return out
}

func (t *Tracker) run() {
	defer close(t.done)
	// Health and maintenance failures are telemetry only; request serving
	// remains fail-open. Both calls have their own deadlines and run even when
	// collection is disabled or its HMAC key is invalid.
	var healthTimer activityTimer
	var healthTicks <-chan time.Time
	var pruneTicker *time.Ticker
	var pruneTicks <-chan time.Time
	markedHealthyDays := make(map[string]struct{})
	if t.maintenanceStore != nil {
		// Arm health checking before the bounded startup calls. The timer wakes
		// at UTC rollover or at the retry bound, whichever comes first. A tick
		// that arrives during startup remains readable, so crossing midnight or
		// a transient startup failure cannot defer the current day until tomorrow.
		healthTimer = t.newHealthTimer(untilNextHealthCheck(t.now()))
		healthTicks = healthTimer.Ticks()
		defer healthTimer.Stop()
		t.markHealthyOnce(context.Background(), markedHealthyDays)
		t.prune(context.Background())
		pruneTicker = time.NewTicker(t.maintenance)
		pruneTicks = pruneTicker.C
		defer pruneTicker.Stop()
	}
	ticker := time.NewTicker(t.flush)
	defer ticker.Stop()
	batch := make([]observation, 0, t.batchSize)
	for {
		select {
		case <-t.stop:
			if t.ready {
				t.drain(batch)
			}
			return
		case obs := <-t.queue:
			batch = append(batch, obs)
			if len(batch) >= t.batchSize {
				t.flushBatch(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				t.flushBatch(batch)
				batch = batch[:0]
			}
		case <-healthTicks:
			t.markHealthyOnce(context.Background(), markedHealthyDays)
			healthTimer.Reset(untilNextHealthCheck(t.now()))
		case <-pruneTicks:
			t.prune(context.Background())
		}
	}
}

func (t *Tracker) drain(batch []observation) {
	for {
		select {
		case obs := <-t.queue:
			batch = append(batch, obs)
			if len(batch) >= t.batchSize {
				t.flushBatch(batch)
				batch = batch[:0]
			}
		default:
			t.flushBatch(batch)
			return
		}
	}
}

func (t *Tracker) flushBatch(batch []observation) {
	if len(batch) == 0 {
		return
	}
	defer t.pending.Add(-int64(len(batch)))
	atRisk := 0
	for _, obs := range batch {
		if t.needsWrite(obs.day) || t.needsWrite(obs.month) {
			atRisk++
		}
	}
	// Coalesce duplicate day/month buckets within the bounded batch.
	unique := make(map[string]Bucket, len(batch)*2)
	for _, obs := range batch {
		for _, b := range []Bucket{obs.day, obs.month} {
			key := b.Kind + "\x00" + b.Epoch + "\x00" + string(b.Value[:])
			if old, ok := unique[key]; ok {
				b.Owner = b.Owner || old.Owner
				if old.SeenAt.Before(b.SeenAt) {
					b.SeenAt = old.SeenAt
				}
			}
			unique[key] = b
		}
	}
	rows := make([]Bucket, 0, len(unique))
	for _, b := range unique {
		if t.needsWrite(b) {
			rows = append(rows, b)
		}
	}
	if len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.dbTimeout)
	defer cancel()
	if err := t.store.RecordActivity(ctx, rows); err != nil {
		t.storeFailures.Add(1)
		t.dropped.Add(uint64(atRisk))
		return
	}
	t.remember(rows)
	t.flushes.Add(1)
}

// needsWrite consults only successfully persisted identities. New identities
// beyond the fixed cap are still written (so the estimate remains complete)
// but are not remembered, keeping memory bounded under hostile cardinality.
func (t *Tracker) needsWrite(b Bucket) bool {
	t.seenMu.Lock()
	defer t.seenMu.Unlock()
	state := t.seenState(b.Kind)
	if state == nil || state.epoch == "" || b.Epoch > state.epoch {
		return true
	}
	if b.Epoch < state.epoch {
		return true
	}
	owner, ok := state.owners[b.Value]
	return !ok || (b.Owner && !owner)
}

func (t *Tracker) remember(rows []Bucket) {
	t.seenMu.Lock()
	defer t.seenMu.Unlock()
	for _, b := range rows {
		state := t.seenState(b.Kind)
		if state == nil || b.Epoch < state.epoch {
			continue
		}
		if b.Epoch > state.epoch {
			state.epoch = b.Epoch
			state.owners = make(map[[16]byte]bool)
		}
		if state.owners == nil {
			state.epoch = b.Epoch
			state.owners = make(map[[16]byte]bool)
		}
		if old, ok := state.owners[b.Value]; ok {
			state.owners[b.Value] = old || b.Owner
		} else if len(state.owners) < t.seenLimit {
			state.owners[b.Value] = b.Owner
		}
	}
}

func (t *Tracker) seenState(kind string) *epochSeen {
	switch kind {
	case KindDay:
		return &t.seenDay
	case KindMonth:
		return &t.seenMonth
	default:
		return nil
	}
}

func (t *Tracker) prune(parent context.Context) {
	if t.maintenanceStore == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, t.dbTimeout)
	defer cancel()
	if err := t.maintenanceStore.PruneActivity(ctx, t.now()); err != nil {
		t.storeFailures.Add(1)
	}
}

func (t *Tracker) markHealthyOnce(parent context.Context, marked map[string]struct{}) {
	if t.maintenanceStore == nil {
		return
	}
	now := t.now()
	epoch := dayEpoch(now)
	if _, ok := marked[epoch]; ok {
		return
	}
	ctx, cancel := context.WithTimeout(parent, t.dbTimeout)
	defer cancel()
	if err := t.maintenanceStore.MarkActivityHealthy(ctx, now); err != nil {
		t.storeFailures.Add(1)
		return
	}
	marked[epoch] = struct{}{}
}

// untilNextUTCDay aligns the health cadence to UTC midnight. A delayed or
// forward-jumped clock writes only the current day when the timer wakes; it
// never backfills skipped epochs and therefore never fabricates historical
// zero-traffic days.
func untilNextUTCDay(now time.Time) time.Duration {
	utc := now.UTC()
	next := time.Date(utc.Year(), utc.Month(), utc.Day()+1, 0, 0, 0, 0, time.UTC)
	return next.Sub(utc)
}

// untilNextHealthCheck keeps failed health writes retryable independently of
// the six-hour prune ticker. Successful days are suppressed by markHealthyOnce,
// so the lightweight wake-up does not produce duplicate database writes.
func untilNextHealthCheck(now time.Time) time.Duration {
	untilRollover := untilNextUTCDay(now)
	if untilRollover < defaultHealthRetry {
		return untilRollover
	}
	return defaultHealthRetry
}

func dayEpoch(t time.Time) string   { return t.UTC().Format("2006-01-02") }
func monthEpoch(t time.Time) string { return t.UTC().Format("2006-01") }

// trustedProxy reports whether the immediate peer is the deployment's own
// reverse proxy. The server's listener is never published to the host or the
// internet — deploy/docker-compose.yml declares `expose` and no `ports` for
// it — so the only peer that can occupy a loopback or private-range position
// on this socket is the proxy container itself.
func trustedProxy(addr netip.Addr) bool {
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// globallyRoutable rejects addresses that cannot identify an external network:
// loopback, private ranges, link-local, multicast, and the unspecified
// address. Accepting one would let every request behind a misconfigured proxy
// collapse into a single bucket and be reported as one external identity.
func globallyRoutable(addr netip.Addr) bool {
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() {
		return false
	}
	return !addr.IsLinkLocalUnicast() && !addr.IsLinkLocalMulticast() &&
		!addr.IsMulticast() && !addr.IsInterfaceLocalMulticast()
}

// requestAddress resolves the network identity at a fail-closed trust
// boundary. Across the boundary the identity is *only* the rightmost
// X-Forwarded-For element, which the trusted proxy appended itself and a
// client therefore cannot control; a missing, malformed, or non-routable
// element means the request is not counted at all rather than being
// attributed to the proxy's own address. Outside the boundary the identity is
// only the real peer address and every forwarding header is ignored, so a
// forged X-Forwarded-For can never move a bucket.
func requestAddress(r *http.Request) (netip.Addr, bool) {
	remote, ok := parseRemoteAddr(r.RemoteAddr)
	if !ok {
		return netip.Addr{}, false
	}
	remote = remote.Unmap()
	if !trustedProxy(remote) {
		if !globallyRoutable(remote) {
			return netip.Addr{}, false
		}
		return remote, true
	}
	return rightmostForwarded(r.Header.Values("X-Forwarded-For"))
}

// rightmostForwarded returns the last element of the last X-Forwarded-For
// header. Go keeps repeated headers as separate values, and the proxy always
// appends to the end, so anything the client supplied sits to the left of it.
func rightmostForwarded(values []string) (netip.Addr, bool) {
	if len(values) == 0 {
		return netip.Addr{}, false
	}
	parts := strings.Split(values[len(values)-1], ",")
	candidate := strings.TrimSpace(parts[len(parts)-1])
	if candidate == "" {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(candidate)
	if err != nil {
		host, _, splitErr := net.SplitHostPort(candidate)
		if splitErr != nil {
			return netip.Addr{}, false
		}
		if addr, err = netip.ParseAddr(host); err != nil {
			return netip.Addr{}, false
		}
	}
	addr = addr.Unmap()
	if !globallyRoutable(addr) {
		return netip.Addr{}, false
	}
	return addr, true
}

func parseRemoteAddr(raw string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(raw); err == nil {
		return ap.Addr(), true
	}
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr, true
	}
	// net.SplitHostPort handles bracketed zones that netip rejected; zones are
	// discarded because they are local interface labels, not network identity.
	if host, _, err := net.SplitHostPort(raw); err == nil {
		if i := strings.LastIndexByte(host, '%'); i >= 0 {
			host = host[:i]
		}
		if addr, err := netip.ParseAddr(host); err == nil {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

func normalizedNetwork(addr netip.Addr) []byte {
	addr = addr.Unmap()
	if addr.Is4() {
		v := addr.As4()
		return append([]byte{4}, v[:]...)
	}
	v := addr.As16()
	for i := 8; i < len(v); i++ {
		v[i] = 0
	}
	return append([]byte{6}, v[:]...)
}

func likelyAutomated(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	for _, marker := range []string{"bot", "crawler", "spider", "monitor", "uptime", "healthcheck", "kube-probe", "prometheus", "pingdom", "statuscake", "headless"} {
		if strings.Contains(ua, marker) {
			return true
		}
	}
	return false
}

func meaningfulRoute(method, path string) bool {
	exact := func(wantMethod, wantPath string) bool {
		methodMatches := method == wantMethod || (wantMethod == http.MethodGet && method == http.MethodHead)
		return methodMatches && path == wantPath
	}
	segments := func(wantMethod, prefix string, min, max int, final string) bool {
		if method != wantMethod && !(wantMethod == http.MethodGet && method == http.MethodHead) {
			return false
		}
		if !strings.HasPrefix(path, prefix) {
			return false
		}
		rest := strings.TrimPrefix(path, prefix)
		if rest == "" || strings.HasSuffix(rest, "/") {
			return false
		}
		parts := strings.Split(rest, "/")
		if len(parts) < min || (max >= 0 && len(parts) > max) {
			return false
		}
		for _, part := range parts {
			if part == "" {
				return false
			}
		}
		return final == "" || parts[len(parts)-1] == final
	}
	return exact(http.MethodPost, "/v1/evidence/batches") ||
		segments(http.MethodGet, "/v1/registry/packages/", 1, 1, "") ||
		segments(http.MethodGet, "/v1/registry/symbols/", 2, -1, "") ||
		exact(http.MethodPost, "/v1/search") ||
		segments(http.MethodGet, "/v1/shards/", 2, -1, "") ||
		exact(http.MethodPost, "/v1/samples") ||
		segments(http.MethodGet, "/v1/samples/", 1, 1, "") ||
		segments(http.MethodGet, "/v1/samples/", 2, 2, "artifact") ||
		exact(http.MethodGet, "/v1/wanted") ||
		exact(http.MethodPost, "/v1/wanted") ||
		exact(http.MethodPost, "/v1/wanted/batches") ||
		exact(http.MethodPost, "/v1/adoptions") ||
		exact(http.MethodPost, "/v1/verifications") ||
		exact(http.MethodGet, "/v1/verification/jobs") ||
		segments(http.MethodPost, "/v1/verification/jobs/", 2, 2, "claim") ||
		exact(http.MethodPost, "/v1/peers/announce") ||
		segments(http.MethodGet, "/v1/peers/for-sample/", 1, 1, "") ||
		exact(http.MethodGet, "/v1/adapters") ||
		exact(http.MethodPost, "/v1/auth/github/device") ||
		exact(http.MethodPost, "/v1/auth/github/poll")
}
