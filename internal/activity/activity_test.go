package activity

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

type memoryStore struct {
	mu          sync.Mutex
	rows        []Bucket
	recordCalls int
	pruneCalls  int
	healthCalls int
	healthTimes []time.Time
	healthOK    []time.Time
	recordErr   error
	pruneErr    error
	healthErr   error
	healthErrs  []error
	recorded    chan struct{}
	pruned      chan time.Time
	healthy     chan time.Time
	pruneBlock  chan struct{}
	healthBlock chan struct{}
	recordBlock chan struct{}
}

func (s *memoryStore) RecordActivity(_ context.Context, rows []Bucket) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	s.mu.Lock()
	s.recordCalls++
	s.rows = append(s.rows, rows...)
	s.mu.Unlock()
	if s.recorded != nil {
		select {
		case s.recorded <- struct{}{}:
		default:
		}
	}
	if s.recordBlock != nil {
		<-s.recordBlock
	}
	return nil
}
func (s *memoryStore) ActivityCounts(context.Context, string, string) (Counts, error) {
	return Counts{}, nil
}
func (s *memoryStore) ActivityDaily(context.Context, string, string) (DailyRaw, error) {
	return DailyRaw{}, nil
}
func (s *memoryStore) PruneActivity(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	s.pruneCalls++
	s.mu.Unlock()
	if s.pruned != nil {
		select {
		case s.pruned <- now:
		default:
		}
	}
	if s.pruneBlock != nil {
		select {
		case <-s.pruneBlock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.pruneErr
}

func (s *memoryStore) MarkActivityHealthy(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	call := s.healthCalls
	s.healthCalls++
	s.healthTimes = append(s.healthTimes, now)
	err := s.healthErr
	if call < len(s.healthErrs) {
		err = s.healthErrs[call]
	}
	s.mu.Unlock()
	if s.healthy != nil {
		select {
		case s.healthy <- now:
		default:
		}
	}
	if s.healthBlock != nil {
		select {
		case <-s.healthBlock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.healthOK = append(s.healthOK, now)
	s.mu.Unlock()
	return nil
}

type manualActivityTimer struct {
	mu        sync.Mutex
	ticks     chan time.Time
	resets    chan time.Duration
	durations []time.Duration
	stopped   bool
}

func newManualActivityTimer(d time.Duration) *manualActivityTimer {
	return &manualActivityTimer{ticks: make(chan time.Time, 1), resets: make(chan time.Duration, 32), durations: []time.Duration{d}}
}

func (t *manualActivityTimer) Ticks() <-chan time.Time { return t.ticks }
func (t *manualActivityTimer) Reset(d time.Duration) {
	t.mu.Lock()
	t.durations = append(t.durations, d)
	t.mu.Unlock()
	select {
	case t.resets <- d:
	default:
	}
}
func (t *manualActivityTimer) Stop() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
}

func awaitTimerReset(t *testing.T, timer *manualActivityTimer) time.Duration {
	t.Helper()
	select {
	case d := <-timer.resets:
		return d
	case <-time.After(time.Second):
		t.Fatal("health timer was not reset")
		return 0
	}
}

func request(remote, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://example.test/v1/search", nil)
	r.RemoteAddr = remote
	r.Header.Set("X-Forwarded-For", xff)
	r.Header.Set("User-Agent", "codesamplex-cli/1")
	return r
}

func TestAddressTrustBoundaryAndIPv6NetworkNormalization(t *testing.T) {
	tracker := New(context.Background(), nil, Config{HashKeyHex: testKey})
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	forged, _ := tracker.observationFor(request("203.0.113.9:1234", "198.51.100.7"), now, false)
	direct, _ := tracker.observationFor(request("203.0.113.9:9999", ""), now, false)
	if forged.day.Value != direct.day.Value {
		t.Fatal("public direct peer was able to forge X-Forwarded-For")
	}

	proxied, _ := tracker.observationFor(request("127.0.0.1:8080", "192.0.2.1, 198.51.100.7"), now, false)
	rightmost, _ := tracker.observationFor(request("127.0.0.1:8080", "198.51.100.7"), now, false)
	leftmost, _ := tracker.observationFor(request("127.0.0.1:8080", "192.0.2.1"), now, false)
	if proxied.day.Value != rightmost.day.Value || proxied.day.Value == leftmost.day.Value {
		t.Fatal("trusted proxy did not use exactly the rightmost forwarded address")
	}

	v6a, _ := tracker.observationFor(request("[2001:db8:abcd:12::1]:443", ""), now, false)
	v6b, _ := tracker.observationFor(request("[2001:db8:abcd:12:ffff::2]:443", ""), now, false)
	v6c, _ := tracker.observationFor(request("[2001:db8:abcd:13::1]:443", ""), now, false)
	if v6a.day.Value != v6b.day.Value || v6a.day.Value == v6c.day.Value {
		t.Fatal("IPv6 identity is not normalized to exactly /64")
	}
}

// The boundary must fail closed in both directions: a forged header from
// outside can never move a bucket, and a request that arrives through the
// proxy without a usable forwarded address is dropped rather than being
// attributed to the proxy itself — which would otherwise collapse all traffic
// behind it into a single bucket and report it as one identity.
func TestTrustBoundaryFailsClosedRatherThanAttributingToTheProxy(t *testing.T) {
	tracker := New(context.Background(), nil, Config{HashKeyHex: testKey})
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	build := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://example.test/v1/search", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		r.Header.Set("User-Agent", "codesamplex-cli/1")
		return r
	}

	refused := []struct {
		name string
		req  *http.Request
	}{
		{"proxy with no forwarded header", build("172.18.0.4:8080")},
		{"proxy with empty forwarded header", build("172.18.0.4:8080", "")},
		{"proxy with malformed forwarded address", build("172.18.0.4:8080", "not-an-address")},
		{"proxy forwarding a private address", build("172.18.0.4:8080", "10.1.2.3")},
		{"proxy forwarding loopback", build("127.0.0.1:8080", "127.0.0.1")},
		{"proxy forwarding the unspecified address", build("172.18.0.4:8080", "0.0.0.0")},
		{"non-routable direct peer", build("192.168.5.5:443")},
	}
	for _, tc := range refused {
		if _, ok := tracker.observationFor(tc.req, now, false); ok {
			t.Errorf("%s produced an identity; boundary is not fail-closed", tc.name)
		}
	}

	// Only the rightmost element — the one the proxy appended itself — counts,
	// across repeated headers as well as a comma list.
	appended, ok := tracker.observationFor(build("172.18.0.4:8080", "198.51.100.7, 203.0.113.9", "203.0.113.200"), now, false)
	if !ok {
		t.Fatal("trusted proxy with a routable forwarded address was refused")
	}
	direct, _ := tracker.observationFor(build("203.0.113.200:1234"), now, false)
	if appended.day.Value != direct.day.Value {
		t.Fatal("rightmost forwarded element is not the identity behind the boundary")
	}
	spoofed, _ := tracker.observationFor(build("203.0.113.200:1234", "198.51.100.7"), now, false)
	if spoofed.day.Value != direct.day.Value {
		t.Fatal("a forged X-Forwarded-For moved the bucket of a direct peer")
	}

	// A refused identity is not silently swallowed: MarkOwner says so.
	if err := New(context.Background(), &memoryStore{}, Config{HashKeyHex: testKey}).
		MarkOwner(context.Background(), build("172.18.0.4:8080", "10.1.2.3"), now); !errors.Is(err, ErrNoNetworkIdentity) {
		t.Fatalf("MarkOwner error = %v, want ErrNoNetworkIdentity", err)
	}
}

func TestDailyWindowNeverInventsAZero(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	window := BuildDailyWindow(now, DailyRaw{
		OldestEpoch: "2026-08-14",
		Days: []DayCount{
			{Epoch: "2026-08-14", Count: 4, Rows: 4, Healthy: true},
			{Epoch: "2026-08-16", Healthy: true},
			{Epoch: "2026-08-17", Count: 1, Rows: 1, Healthy: true},
		},
	})
	if len(window.Points) != DailyWindowDays {
		t.Fatalf("points = %d, want %d", len(window.Points), DailyWindowDays)
	}
	if window.Points[0].Epoch != "2026-07-18" || window.Points[DailyWindowDays-1].Epoch != "2026-08-17" {
		t.Fatalf("window range = %s~%s", window.Points[0].Epoch, window.Points[DailyWindowDays-1].Epoch)
	}
	for _, p := range window.Points {
		switch p.Epoch {
		case "2026-08-14", "2026-08-17":
			if p.BeforeCollection || p.Gap || p.Count == 0 {
				t.Errorf("collected day %s = %+v", p.Epoch, p)
			}
		case "2026-08-16":
			if !p.HealthyZero || p.Gap || p.BeforeCollection || p.Count != 0 {
				t.Errorf("health-proven zero day %s = %+v", p.Epoch, p)
			}
		case "2026-08-15":
			if !p.Gap || p.HealthyZero || p.BeforeCollection {
				t.Errorf("collection gap day %s = %+v", p.Epoch, p)
			}
		default:
			if !p.BeforeCollection || p.Gap || p.Count != 0 {
				t.Errorf("pre-collection day %s = %+v", p.Epoch, p)
			}
		}
	}
	if window.Gaps != 1 || window.Max != 4 || window.StartEpoch != "2026-08-14" || !window.Collecting {
		t.Fatalf("window summary = gaps:%d max:%d start:%s collecting:%v", window.Gaps, window.Max, window.StartEpoch, window.Collecting)
	}

	// Retention reaching back past the chart means the chart's own left edge is
	// the honest collection start to report, not a day it cannot draw.
	deep := BuildDailyWindow(now, DailyRaw{OldestEpoch: "2026-07-14", Days: []DayCount{{Epoch: "2026-08-17", Count: 1, Rows: 1, Healthy: true}}})
	if deep.StartEpoch != "2026-07-18" || deep.Gaps != DailyWindowDays-1 {
		t.Fatalf("deep-retention window = start:%s gaps:%d", deep.StartEpoch, deep.Gaps)
	}
}

func TestBucketsAreIndependentAcrossKindAndEpoch(t *testing.T) {
	tracker := New(context.Background(), nil, Config{HashKeyHex: testKey})
	r := request("198.51.100.7:443", "")
	one, _ := tracker.observationFor(r, time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC), false)
	two, _ := tracker.observationFor(r, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), false)
	nextMonth, _ := tracker.observationFor(r, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), false)
	if one.day.Value == two.day.Value || one.day.Value == one.month.Value || one.month.Value == nextMonth.month.Value {
		t.Fatal("bucket can be linked across kind or epoch")
	}
	if len(one.day.Value) != 16 {
		t.Fatalf("bucket bytes = %d, want 16", len(one.day.Value))
	}
}

func TestPersistenceBoundaryHasNoPIIFields(t *testing.T) {
	typ := reflect.TypeOf(Bucket{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		for _, forbidden := range []string{"ip", "forward", "agent", "route", "path", "param", "identifier"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("persistence field %q contains forbidden PII surface", name)
			}
		}
	}
	if raw, _ := hex.DecodeString(testKey); len(raw) != 32 {
		t.Fatal("test key is not 256 bit")
	}
}

func TestMeaningfulRoutesAndBots(t *testing.T) {
	allowed := [][2]string{{http.MethodPost, "/v1/search"}, {http.MethodGet, "/v1/registry/packages/pkg%3Anpm%2Faxios%401.0.0"}, {http.MethodGet, "/v1/samples/id"}, {http.MethodHead, "/v1/samples/id"}, {http.MethodGet, "/v1/samples/id/artifact"}, {http.MethodPost, "/v1/verification/jobs/7/claim"}}
	for _, pair := range allowed {
		if !meaningfulRoute(pair[0], pair[1]) {
			t.Errorf("meaningful route excluded: %v", pair)
		}
	}
	excluded := [][2]string{{http.MethodGet, "/admin"}, {http.MethodGet, "/healthz"}, {http.MethodGet, "/"}, {http.MethodGet, "/v1/stats"}, {http.MethodGet, "/v1/unknown"}, {http.MethodGet, "/v1/samples/id/extra"}, {http.MethodGet, "/v1/search"}}
	for _, pair := range excluded {
		if meaningfulRoute(pair[0], pair[1]) {
			t.Errorf("excluded route counted: %v", pair)
		}
	}
	for _, ua := range []string{"Googlebot/2.1", "Uptime-Monitor", "kube-probe/1.30", "HeadlessChrome"} {
		if !likelyAutomated(ua) {
			t.Errorf("bot UA not excluded: %q", ua)
		}
	}
	if likelyAutomated("codesamplex-cli/1.0") {
		t.Fatal("normal client classified as bot")
	}
}

// One network active in one day/month period costs exactly two upserts no
// matter how many requests it makes, and a successful flush is counted.
func TestRepeatedTrafficCostsAtMostTwoUpsertsAndCountsTheFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &memoryStore{recorded: make(chan struct{}, 1)}
	tracker := New(ctx, store, Config{
		HashKeyHex: testKey, QueueSize: 64, BatchSize: 1, FlushEvery: time.Hour,
		Now: func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) },
	})
	h := tracker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for i := 0; i < 8; i++ {
		h.ServeHTTP(httptest.NewRecorder(), request("198.51.100.7:443", ""))
	}
	select {
	case <-store.recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("batch never reached the store")
	}
	store.mu.Lock()
	rows := append([]Bucket(nil), store.rows...)
	store.mu.Unlock()
	if len(rows) != 2 {
		t.Fatalf("upserts for one network in one period = %d, want 2 (one day, one month)", len(rows))
	}
	if rows[0].Kind == rows[1].Kind {
		t.Fatalf("both upserts were %q; want one day and one month", rows[0].Kind)
	}
	deadline := time.Now().Add(2 * time.Second)
	for tracker.Telemetry().Flushes == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	telemetry := tracker.Telemetry()
	if telemetry.Flushes != 1 || telemetry.StoreFailures != 0 || telemetry.Dropped != 0 {
		t.Fatalf("telemetry = %+v, want exactly one successful flush", telemetry)
	}
	store.mu.Lock()
	recordCalls := store.recordCalls
	store.mu.Unlock()
	if recordCalls != 1 {
		t.Fatalf("store calls across eight one-observation batches = %d, want 1", recordCalls)
	}
}

func TestCrossBatchDedupeResetsPerEpochAndPreservesOwnerPromotion(t *testing.T) {
	var nowMu sync.RWMutex
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	store := &memoryStore{}
	tracker := New(context.Background(), store, Config{
		HashKeyHex: testKey, QueueSize: 16, BatchSize: 1, FlushEvery: time.Hour, SeenLimit: 8,
		Now: func() time.Time { nowMu.RLock(); defer nowMu.RUnlock(); return now },
	})
	defer tracker.Close(context.Background())
	h := tracker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	sendAndDrain := func() {
		h.ServeHTTP(httptest.NewRecorder(), request("198.51.100.7:443", ""))
		deadline := time.Now().Add(time.Second)
		for tracker.Telemetry().Pending != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if tracker.Telemetry().Pending != 0 {
			t.Fatal("activity observation did not drain")
		}
	}
	sendAndDrain() // day + month
	sendAndDrain() // fully deduped across a batch boundary

	if err := tracker.MarkOwner(context.Background(), request("198.51.100.7:443", ""), now); err != nil {
		t.Fatal(err)
	}
	sendAndDrain() // owner cache suppresses a later non-owner observation

	nowMu.Lock()
	now = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	nowMu.Unlock()
	sendAndDrain() // new day, same month: one new upsert

	nowMu.Lock()
	now = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	nowMu.Unlock()
	sendAndDrain() // new day and month: two new upserts

	store.mu.Lock()
	rows := append([]Bucket(nil), store.rows...)
	calls := store.recordCalls
	store.mu.Unlock()
	if len(rows) != 7 || calls != 4 {
		t.Fatalf("epoch/owner upserts rows=%d calls=%d, want 7 rows in 4 calls", len(rows), calls)
	}
	if !rows[2].Owner || !rows[3].Owner {
		t.Fatalf("owner promotion was not persisted: %+v", rows[2:4])
	}
	tracker.seenMu.Lock()
	dayEpoch, monthEpoch := tracker.seenDay.epoch, tracker.seenMonth.epoch
	daySize, monthSize := len(tracker.seenDay.owners), len(tracker.seenMonth.owners)
	tracker.seenMu.Unlock()
	if dayEpoch != "2026-09-01" || monthEpoch != "2026-09" || daySize > 8 || monthSize > 8 {
		t.Fatalf("bounded epoch cache day=%s/%d month=%s/%d", dayEpoch, daySize, monthEpoch, monthSize)
	}
}

func TestMaintenanceRunsAtStartupAndPeriodicallyWithoutAValidCollectorKey(t *testing.T) {
	fixedNow := time.Date(2026, 8, 17, 23, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		key  string
	}{{"missing", ""}, {"malformed", "malformed"}} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{healthy: make(chan time.Time, 1), pruned: make(chan time.Time, 4)}
			tracker := New(context.Background(), store, Config{
				HashKeyHex: tc.key, DBTimeout: 20 * time.Millisecond, MaintenanceEvery: 5 * time.Millisecond,
				Now: func() time.Time { return fixedNow },
			})
			defer tracker.Close(context.Background())
			select {
			case got := <-store.healthy:
				if got != fixedNow {
					t.Fatalf("startup health time = %v, want %v", got, fixedNow)
				}
			case <-time.After(time.Second):
				t.Fatal("startup health marker did not run")
			}
			for phase := 0; phase < 2; phase++ {
				select {
				case <-store.pruned:
				case <-time.After(time.Second):
					t.Fatalf("maintenance phase %d did not run", phase)
				}
			}
			store.mu.Lock()
			healthCalls := store.healthCalls
			store.mu.Unlock()
			if healthCalls != 1 {
				t.Fatalf("same-day health writes = %d, want exactly startup write", healthCalls)
			}
			if tracker.Telemetry().StoreFailures != 0 {
				t.Fatalf("successful maintenance reported failures: %+v", tracker.Telemetry())
			}
		})
	}
}

func TestMaintenanceDoesNotDependOnTheCollectorStoreContract(t *testing.T) {
	store := &memoryStore{healthy: make(chan time.Time, 1), pruned: make(chan time.Time, 4)}
	tracker := NewWithMaintenance(context.Background(), nil, store, Config{MaintenanceEvery: 5 * time.Millisecond})
	defer tracker.Close(context.Background())
	if tracker.Available() {
		t.Fatal("maintenance-only store unexpectedly enabled collection")
	}
	select {
	case <-store.healthy:
	case <-time.After(time.Second):
		t.Fatal("maintenance-only startup health did not run")
	}
	for phase := 0; phase < 2; phase++ {
		select {
		case <-store.pruned:
		case <-time.After(time.Second):
			t.Fatalf("maintenance-only prune phase %d did not run", phase)
		}
	}
}

func TestHealthTimerUsesTheNextUTCMidnight(t *testing.T) {
	kst := time.FixedZone("KST", 9*60*60)
	for _, tc := range []struct {
		name string
		now  time.Time
		want time.Duration
	}{
		{"utc near rollover", time.Date(2026, 8, 17, 23, 59, 30, 0, time.UTC), 30 * time.Second},
		{"non-utc clock", time.Date(2026, 8, 18, 8, 59, 30, 0, kst), 30 * time.Second},
		{"utc midday", time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC), 12 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := untilNextUTCDay(tc.now); got != tc.want {
				t.Fatalf("until next UTC day = %v, want %v", got, tc.want)
			}
		})
	}
	if defaultHealthRetry >= defaultMaintenance {
		t.Fatalf("health retry cadence %v must be shorter than prune cadence %v", defaultHealthRetry, defaultMaintenance)
	}
	if got := untilNextHealthCheck(time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)); got != defaultHealthRetry {
		t.Fatalf("midday health check delay = %v, want retry cadence %v", got, defaultHealthRetry)
	}
	if got := untilNextHealthCheck(time.Date(2026, 8, 17, 23, 59, 30, 0, time.UTC)); got != 30*time.Second {
		t.Fatalf("near-midnight health check delay = %v, want 30s", got)
	}
}

func TestRolloverQueuedDuringStartupIsMarkedAfterBoundedMaintenance(t *testing.T) {
	var unixNano atomic.Int64
	before := time.Date(2026, 8, 17, 23, 59, 59, 0, time.UTC)
	after := time.Date(2026, 8, 18, 0, 0, 1, 0, time.UTC)
	unixNano.Store(before.UnixNano())
	now := func() time.Time { return time.Unix(0, unixNano.Load()).UTC() }

	releaseHealth := make(chan struct{})
	store := &memoryStore{
		healthy: make(chan time.Time, 2), pruned: make(chan time.Time, 1), healthBlock: releaseHealth,
	}
	created := make(chan *manualActivityTimer, 1)
	tracker := New(context.Background(), store, Config{
		MaintenanceEvery: time.Hour,
		Now:              now,
		newHealthTimer: func(d time.Duration) activityTimer {
			timer := newManualActivityTimer(d)
			created <- timer
			return timer
		},
	})
	defer tracker.Close(context.Background())

	var timer *manualActivityTimer
	select {
	case timer = <-created:
	case <-time.After(time.Second):
		t.Fatal("rollover timer was not armed before startup maintenance")
	}
	select {
	case got := <-store.healthy:
		if dayEpoch(got) != "2026-08-17" {
			t.Fatalf("startup marker epoch = %s", dayEpoch(got))
		}
	case <-time.After(time.Second):
		t.Fatal("startup health call did not begin")
	}
	unixNano.Store(after.UnixNano())
	timer.ticks <- after
	close(releaseHealth)
	select {
	case <-store.pruned:
	case <-time.After(time.Second):
		t.Fatal("startup prune did not complete")
	}
	select {
	case got := <-store.healthy:
		if dayEpoch(got) != "2026-08-18" {
			t.Fatalf("queued rollover marker epoch = %s", dayEpoch(got))
		}
	case <-time.After(time.Second):
		t.Fatal("queued startup rollover was not marked")
	}
	if resetDelay := awaitTimerReset(t, timer); resetDelay != defaultHealthRetry {
		t.Fatalf("startup-crossing reset delay = %v, want %v", resetDelay, defaultHealthRetry)
	}
	store.mu.Lock()
	healthCalls, pruneCalls := store.healthCalls, store.pruneCalls
	store.mu.Unlock()
	if healthCalls != 2 || pruneCalls != 1 {
		t.Fatalf("startup rollover calls = health:%d prune:%d, want 2/1", healthCalls, pruneCalls)
	}
}

func TestStartupHealthFailureRetriesUntilTheCurrentDaySucceedsOnce(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	transient := errors.New("temporary health failure")
	store := &memoryStore{
		healthy:    make(chan time.Time, 4),
		pruned:     make(chan time.Time, 1),
		healthErrs: []error{transient, transient, nil},
	}
	created := make(chan *manualActivityTimer, 1)
	tracker := New(context.Background(), store, Config{
		Now: func() time.Time { return now },
		newHealthTimer: func(d time.Duration) activityTimer {
			timer := newManualActivityTimer(d)
			created <- timer
			return timer
		},
	})
	defer tracker.Close(context.Background())

	timer := <-created
	if initial := timer.durations[0]; initial != defaultHealthRetry {
		t.Fatalf("initial retry delay = %v, want %v", initial, defaultHealthRetry)
	}
	select {
	case <-store.healthy:
	case <-time.After(time.Second):
		t.Fatal("startup health attempt did not run")
	}
	select {
	case <-store.pruned:
	case <-time.After(time.Second):
		t.Fatal("startup prune did not run")
	}

	for retry := 1; retry <= 2; retry++ {
		timer.ticks <- now
		select {
		case got := <-store.healthy:
			if epoch := dayEpoch(got); epoch != "2026-08-17" {
				t.Fatalf("retry %d epoch = %s", retry, epoch)
			}
		case <-time.After(time.Second):
			t.Fatalf("retry %d did not run", retry)
		}
		if reset := awaitTimerReset(t, timer); reset != defaultHealthRetry {
			t.Fatalf("retry %d reset = %v, want %v", retry, reset, defaultHealthRetry)
		}
	}

	// Once the day succeeds, another cadence wake is observable through Reset
	// but must not issue a duplicate successful database call.
	timer.ticks <- now
	if reset := awaitTimerReset(t, timer); reset != defaultHealthRetry {
		t.Fatalf("post-success reset = %v, want %v", reset, defaultHealthRetry)
	}
	select {
	case got := <-store.healthy:
		t.Fatalf("duplicate health write after success at %v", got)
	default:
	}
	store.mu.Lock()
	healthCalls := store.healthCalls
	healthOK := append([]time.Time(nil), store.healthOK...)
	pruneCalls := store.pruneCalls
	store.mu.Unlock()
	if healthCalls != 3 || len(healthOK) != 1 || dayEpoch(healthOK[0]) != "2026-08-17" {
		t.Fatalf("startup retry results = calls:%d successes:%v, want 3 calls and one current-day success", healthCalls, healthOK)
	}
	if pruneCalls != 1 {
		t.Fatalf("health retries triggered %d prune calls, want startup-only prune", pruneCalls)
	}
	if failures := tracker.Telemetry().StoreFailures; failures != 2 {
		t.Fatalf("health retry failures = %d, want 2", failures)
	}
}

func TestMidnightHealthFailureRetriesThenSuppressesDuplicateSuccess(t *testing.T) {
	var unixNano atomic.Int64
	setNow := func(v time.Time) { unixNano.Store(v.UnixNano()) }
	now := func() time.Time { return time.Unix(0, unixNano.Load()).UTC() }
	setNow(time.Date(2026, 8, 17, 23, 59, 30, 0, time.UTC))
	transient := errors.New("midnight database restart")
	store := &memoryStore{
		healthy:    make(chan time.Time, 4),
		pruned:     make(chan time.Time, 1),
		healthErrs: []error{nil, transient, nil},
	}
	created := make(chan *manualActivityTimer, 1)
	tracker := New(context.Background(), store, Config{
		Now: now,
		newHealthTimer: func(d time.Duration) activityTimer {
			timer := newManualActivityTimer(d)
			created <- timer
			return timer
		},
	})
	defer tracker.Close(context.Background())

	timer := <-created
	select {
	case <-store.healthy:
	case <-time.After(time.Second):
		t.Fatal("startup health marker did not run")
	}
	select {
	case <-store.pruned:
	case <-time.After(time.Second):
		t.Fatal("startup prune did not run")
	}
	setNow(time.Date(2026, 8, 18, 0, 0, 1, 0, time.UTC))

	for attempt := 1; attempt <= 2; attempt++ {
		timer.ticks <- now()
		select {
		case got := <-store.healthy:
			if epoch := dayEpoch(got); epoch != "2026-08-18" {
				t.Fatalf("midnight attempt %d epoch = %s", attempt, epoch)
			}
		case <-time.After(time.Second):
			t.Fatalf("midnight attempt %d did not run", attempt)
		}
		if reset := awaitTimerReset(t, timer); reset != defaultHealthRetry {
			t.Fatalf("midnight attempt %d reset = %v, want %v", attempt, reset, defaultHealthRetry)
		}
	}
	timer.ticks <- now()
	_ = awaitTimerReset(t, timer)
	select {
	case got := <-store.healthy:
		t.Fatalf("duplicate midnight success at %v", got)
	default:
	}

	store.mu.Lock()
	healthOK := append([]time.Time(nil), store.healthOK...)
	healthCalls := store.healthCalls
	store.mu.Unlock()
	if healthCalls != 3 || len(healthOK) != 2 || dayEpoch(healthOK[0]) != "2026-08-17" || dayEpoch(healthOK[1]) != "2026-08-18" {
		t.Fatalf("midnight retry results = calls:%d successes:%v", healthCalls, healthOK)
	}
	if failures := tracker.Telemetry().StoreFailures; failures != 1 {
		t.Fatalf("midnight retry failures = %d, want 1", failures)
	}
}

func TestHealthClockJumpsNeverBackfillOrRepeatSuccessfulDays(t *testing.T) {
	var unixNano atomic.Int64
	setNow := func(v time.Time) { unixNano.Store(v.UnixNano()) }
	now := func() time.Time { return time.Unix(0, unixNano.Load()).UTC() }
	setNow(time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	store := &memoryStore{healthy: make(chan time.Time, 4), pruned: make(chan time.Time, 1)}
	created := make(chan *manualActivityTimer, 1)
	tracker := New(context.Background(), store, Config{
		Now: now,
		newHealthTimer: func(d time.Duration) activityTimer {
			timer := newManualActivityTimer(d)
			created <- timer
			return timer
		},
	})
	defer tracker.Close(context.Background())
	var timer *manualActivityTimer
	select {
	case timer = <-created:
	case <-time.After(time.Second):
		t.Fatal("health timer was not created")
	}
	select {
	case <-store.healthy:
	case <-time.After(time.Second):
		t.Fatal("startup health marker did not run")
	}
	select {
	case <-store.pruned:
	case <-time.After(time.Second):
		t.Fatal("startup prune did not run")
	}

	fire := func(at time.Time, wantCall bool) {
		t.Helper()
		setNow(at)
		timer.ticks <- at
		if wantCall {
			select {
			case got := <-store.healthy:
				if dayEpoch(got) != dayEpoch(at) {
					t.Fatalf("health epoch = %s, want %s", dayEpoch(got), dayEpoch(at))
				}
			case <-time.After(time.Second):
				t.Fatalf("health call for %s did not run", dayEpoch(at))
			}
		}
		_ = awaitTimerReset(t, timer)
		if !wantCall {
			select {
			case got := <-store.healthy:
				t.Fatalf("unexpected repeated health call for %s at %v", dayEpoch(at), got)
			default:
			}
		}
	}

	// The jump skips the 18th and writes only the currently observed 19th.
	fire(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), true)
	// Rewinding to an already successful day and returning to the future day
	// cannot duplicate either successful write.
	fire(time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC), false)
	fire(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), false)
	fire(time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), true)

	store.mu.Lock()
	healthOK := append([]time.Time(nil), store.healthOK...)
	store.mu.Unlock()
	want := []string{"2026-08-17", "2026-08-19", "2026-08-20"}
	if len(healthOK) != len(want) {
		t.Fatalf("successful health days = %v, want %v", healthOK, want)
	}
	for i, got := range healthOK {
		if epoch := dayEpoch(got); epoch != want[i] {
			t.Fatalf("successful health day %d = %s, want %s", i, epoch, want[i])
		}
	}
}

func TestHealthMarkerAlignsToUTCRolloverWithoutBackfillingOrExtraPruning(t *testing.T) {
	var unixNano atomic.Int64
	setNow := func(v time.Time) { unixNano.Store(v.UnixNano()) }
	now := func() time.Time { return time.Unix(0, unixNano.Load()).UTC() }
	setNow(time.Date(2026, 8, 17, 23, 59, 30, 0, time.UTC))

	store := &memoryStore{healthy: make(chan time.Time, 4), pruned: make(chan time.Time, 1)}
	created := make(chan *manualActivityTimer, 1)
	tracker := New(context.Background(), store, Config{
		MaintenanceEvery: time.Hour,
		Now:              now,
		newHealthTimer: func(d time.Duration) activityTimer {
			timer := newManualActivityTimer(d)
			created <- timer
			return timer
		},
	})

	assertHealth := func(want string) {
		t.Helper()
		select {
		case got := <-store.healthy:
			if epoch := dayEpoch(got); epoch != want {
				t.Fatalf("health epoch = %s, want %s", epoch, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("health marker %s did not run", want)
		}
	}
	assertHealth("2026-08-17")
	select {
	case <-store.pruned:
	case <-time.After(time.Second):
		t.Fatal("startup pruning did not run")
	}
	var timer *manualActivityTimer
	select {
	case timer = <-created:
	case <-time.After(time.Second):
		t.Fatal("rollover timer was not created")
	}
	timer.mu.Lock()
	initialDelay := timer.durations[0]
	timer.mu.Unlock()
	if initialDelay != 30*time.Second {
		t.Fatalf("initial rollover delay = %v, want 30s", initialDelay)
	}

	setNow(time.Date(2026, 8, 18, 0, 0, 1, 0, time.UTC))
	timer.ticks <- now()
	assertHealth("2026-08-18")
	if resetDelay := awaitTimerReset(t, timer); resetDelay != defaultHealthRetry {
		t.Fatalf("health reset delay = %v, want %v", resetDelay, defaultHealthRetry)
	}

	// A forward clock jump records only the day observed at wake-up. The
	// skipped historical day remains unknown rather than becoming a fake zero.
	setNow(time.Date(2026, 8, 20, 0, 0, 2, 0, time.UTC))
	timer.ticks <- now()
	assertHealth("2026-08-20")
	if resetDelay := awaitTimerReset(t, timer); resetDelay != defaultHealthRetry {
		t.Fatalf("forward-jump reset delay = %v, want %v", resetDelay, defaultHealthRetry)
	}
	store.mu.Lock()
	healthCalls, pruneCalls := store.healthCalls, store.pruneCalls
	store.mu.Unlock()
	if healthCalls != 3 || pruneCalls != 1 {
		t.Fatalf("bounded calls = health:%d prune:%d, want 3 markers and startup-only prune", healthCalls, pruneCalls)
	}

	if err := tracker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	timer.mu.Lock()
	stopped := timer.stopped
	timer.mu.Unlock()
	if !stopped {
		t.Fatal("rollover timer was not stopped during shutdown")
	}
	timer.ticks <- now()
	store.mu.Lock()
	closedHealthCalls, closedPruneCalls := store.healthCalls, store.pruneCalls
	store.mu.Unlock()
	if closedHealthCalls != healthCalls || closedPruneCalls != pruneCalls {
		t.Fatalf("maintenance continued after shutdown: health=%d prune=%d", closedHealthCalls, closedPruneCalls)
	}
}

func TestMaintenanceTimeoutIsBoundedWhenCollectionIsUnavailable(t *testing.T) {
	store := &memoryStore{pruneBlock: make(chan struct{})}
	tracker := New(context.Background(), store, Config{
		DBTimeout: 10 * time.Millisecond, MaintenanceEvery: time.Hour,
	})
	defer tracker.Close(context.Background())
	deadline := time.Now().Add(time.Second)
	for tracker.Telemetry().StoreFailures == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if tracker.Telemetry().StoreFailures != 1 {
		t.Fatalf("bounded startup maintenance telemetry = %+v, want one timeout", tracker.Telemetry())
	}
}

func TestQueueSaturationNeverDelaysResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	block := make(chan struct{})
	store := &memoryStore{pruneBlock: block}
	tracker := New(ctx, store, Config{HashKeyHex: testKey, QueueSize: 1, FlushEvery: time.Hour})
	h := tracker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	start := time.Now()
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request("198.51.100.7:443", ""))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("response %d failed under saturation", i)
		}
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("saturated tracking delayed responses by %v", elapsed)
	}
	if tracker.telemetry().Dropped == 0 {
		t.Fatal("queue saturation was not observable")
	}
	close(block)
	cancel()
}

func TestDBFailureIsFailOpenAndObservable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &memoryStore{recordErr: errors.New("database down")}
	tracker := New(ctx, store, Config{HashKeyHex: testKey, QueueSize: 2, BatchSize: 1, FlushEvery: time.Millisecond})
	h := tracker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request("198.51.100.7:443", ""))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("API status = %d, want fail-open 202", rec.Code)
	}
	deadline := time.Now().Add(time.Second)
	for tracker.telemetry().StoreFailures == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if tracker.telemetry().StoreFailures == 0 {
		t.Fatal("database failure was not observable")
	}
	deadline = time.Now().Add(time.Second)
	for tracker.telemetry().Pending != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	telemetry := tracker.Telemetry()
	if telemetry.Pending != 0 || telemetry.Dropped != 1 {
		t.Fatalf("failed persistence telemetry = %+v, want pending=0 dropped=1", telemetry)
	}
}

func TestPendingIncludesBatchStagedForPersistence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorded := make(chan struct{}, 1)
	release := make(chan struct{})
	store := &memoryStore{recorded: recorded, recordBlock: release}
	tracker := New(ctx, store, Config{HashKeyHex: testKey, QueueSize: 2, BatchSize: 1, FlushEvery: time.Hour})
	h := tracker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	h.ServeHTTP(httptest.NewRecorder(), request("198.51.100.7:443", ""))
	select {
	case <-recorded:
	case <-time.After(time.Second):
		t.Fatal("activity batch never reached persistence")
	}
	if got := tracker.Telemetry().Pending; got != 1 {
		t.Fatalf("pending while batch is staged = %d, want 1", got)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for tracker.Telemetry().Pending != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := tracker.Telemetry().Pending; got != 0 {
		t.Fatalf("pending after persistence = %d, want 0", got)
	}
}

func TestCloseStopsAdmissionCountsLateObservationsAndDrains(t *testing.T) {
	recorded := make(chan struct{}, 1)
	release := make(chan struct{})
	store := &memoryStore{recorded: recorded, recordBlock: release}
	tracker := New(context.Background(), store, Config{HashKeyHex: testKey, QueueSize: 8, BatchSize: 1, FlushEvery: time.Hour})
	h := tracker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	h.ServeHTTP(httptest.NewRecorder(), request("198.51.100.7:443", ""))
	select {
	case <-recorded:
	case <-time.After(time.Second):
		t.Fatal("first activity observation never reached persistence")
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- tracker.Close(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for {
		tracker.gate.RLock()
		accepting := tracker.accepting
		tracker.gate.RUnlock()
		if !accepting || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 7; i++ {
		h.ServeHTTP(httptest.NewRecorder(), request("198.51.100.7:443", ""))
	}
	if got := tracker.Telemetry(); got.Pending != 1 || got.Dropped != 7 {
		t.Fatalf("telemetry while close is draining = %+v, want pending=1 dropped=7", got)
	}
	close(release)
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after persistence was released")
	}
	if got := tracker.Telemetry(); got.Pending != 0 || got.Dropped != 7 || got.StoreFailures != 0 {
		t.Fatalf("final close telemetry = %+v", got)
	}
}

func TestCloseTimeoutKeepsEveryUnflushedObservationPending(t *testing.T) {
	recorded := make(chan struct{}, 1)
	release := make(chan struct{})
	store := &memoryStore{recorded: recorded, recordBlock: release}
	tracker := New(context.Background(), store, Config{HashKeyHex: testKey, QueueSize: 8, BatchSize: 4, FlushEvery: time.Hour})
	h := tracker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for i := 0; i < 4; i++ {
		h.ServeHTTP(httptest.NewRecorder(), request("198.51.100.7:443", ""))
	}
	select {
	case <-recorded:
	case <-time.After(time.Second):
		t.Fatal("staged batch never reached persistence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := tracker.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want deadline", err)
	}
	if got := tracker.Telemetry(); got.Pending != 4 || got.Dropped != 0 {
		t.Fatalf("timed-out close telemetry = %+v, want all four pending", got)
	}
	close(release)
	if err := tracker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Telemetry(); got.Pending != 0 || got.Dropped != 0 {
		t.Fatalf("retried close telemetry = %+v", got)
	}
}

func TestCloseWaitsForHandlersAdmittedBeforeCollectorCutoff(t *testing.T) {
	tracker := New(context.Background(), &memoryStore{}, Config{HashKeyHex: testKey, QueueSize: 2, BatchSize: 1})
	entered := make(chan struct{})
	release := make(chan struct{})
	h := tracker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	requestDone := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), request("198.51.100.7:443", ""))
		close(requestDone)
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := tracker.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close while admitted handler is running = %v, want deadline", err)
	}
	close(release)
	<-requestDone
	if err := tracker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Telemetry(); got.Pending != 0 || got.Dropped != 0 || got.StoreFailures != 0 {
		t.Fatalf("telemetry after admitted handler drain = %+v", got)
	}
}

func TestInvalidKeyDisablesTracking(t *testing.T) {
	for _, key := range []string{"short", strings.Repeat("z", 64), strings.Repeat("0", 62)} {
		tracker := New(context.Background(), &memoryStore{}, Config{HashKeyHex: key})
		defer tracker.Close(context.Background())
		if tracker.Available() || !tracker.ConfigurationInvalid() {
			t.Errorf("malformed key %q was accepted", key)
		}
	}
}

func TestMissingKeyIsExplicitlyUnavailable(t *testing.T) {
	tracker := New(context.Background(), &memoryStore{}, Config{})
	defer tracker.Close(context.Background())
	if tracker.Available() || tracker.ConfigurationInvalid() {
		t.Fatal("missing key must be unavailable without being reported as malformed")
	}
	if _, err := tracker.Metrics(context.Background(), time.Now()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing-key Metrics error = %v, want ErrUnavailable", err)
	}
	if err := tracker.MarkOwner(context.Background(), request("198.51.100.7:443", ""), time.Now()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing-key MarkOwner error = %v, want ErrUnavailable", err)
	}
}
