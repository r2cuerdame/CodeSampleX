package serverstore

// The policy arithmetic, checked without a database. These are the numbers
// the shipped server runs on, so a change to any of them has to be a
// deliberate edit here and not a side effect somewhere else.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// The whole point of the class caps: whatever the other classes are doing,
// every class still has connections it cannot be deprived of. Stated as the
// arithmetic rather than as three numbers, because it is the arithmetic that
// has to keep holding when someone tunes one of them.
func TestShippedPolicyGuaranteesEveryClassAFloor(t *testing.T) {
	pol := DefaultPoolPolicy().normalize()
	general := pol.general()

	probeFloor := pol.MaxConns - general
	interactiveFloor := general - pol.BackgroundConns
	backgroundFloor := general - pol.InteractiveConns

	if probeFloor < 1 {
		t.Errorf("the health probe can be starved: pool %d, general %d", pol.MaxConns, general)
	}
	if interactiveFloor < 1 {
		t.Errorf("page reads can be starved by ingest: general %d, background cap %d", general, pol.BackgroundConns)
	}
	if backgroundFloor < 1 {
		t.Errorf("ingest can be starved by page reads: general %d, read cap %d", general, pol.InteractiveConns)
	}
	// Caps that summed to the pool would be a partition, and a partition
	// wastes whatever the quiet classes are not using.
	if pol.InteractiveConns+pol.BackgroundConns <= general {
		t.Errorf("the class caps partition the pool instead of overlapping: %d + %d <= %d",
			pol.InteractiveConns, pol.BackgroundConns, general)
	}
}

// A read's worst case has to stay well under the 60s WriteTimeout that
// turned the R2C-55 incident into 502s, and the probe's worst case under the
// 3s deadline handleHealthz sets on itself.
func TestShippedBudgetsFitInsideTheHTTPDeadlines(t *testing.T) {
	pol := DefaultPoolPolicy()
	if worst := pol.ReadWait + pol.ReadTimeout; worst >= 60*time.Second {
		t.Errorf("a read can hold a connection for %v, which is the WriteTimeout that produced the 502s", worst)
	}
	if worst := pol.ProbeWait + pol.ProbeTimeout; worst > 3*time.Second {
		t.Errorf("the probe's budgets total %v but handleHealthz gives it 3s", worst)
	}
}

func TestNormalizeClampsAPolicyThatCouldDeadlockTheServer(t *testing.T) {
	cases := []struct {
		name string
		in   PoolPolicy
		want func(PoolPolicy) error
	}{
		{
			name: "a reserve that swallows the pool leaves one general connection",
			in:   PoolPolicy{Enabled: true, MaxConns: 4, ProbeReserve: 9},
			want: func(p PoolPolicy) error {
				if p.general() != 1 {
					return errors.New("nothing but probes could run")
				}
				return nil
			},
		},
		{
			name: "a class cap above the pool becomes the pool",
			in:   PoolPolicy{Enabled: true, MaxConns: 4, ProbeReserve: 1, InteractiveConns: 99},
			want: func(p PoolPolicy) error {
				if p.InteractiveConns != p.general() {
					return errors.New("the gate would be wider than the pool")
				}
				return nil
			},
		},
		{
			name: "a zero class cap becomes the pool, not a closed gate",
			in:   PoolPolicy{Enabled: true, MaxConns: 4, ProbeReserve: 1},
			want: func(p PoolPolicy) error {
				if p.InteractiveConns < 1 || p.BackgroundConns < 1 {
					return errors.New("a class was locked out entirely")
				}
				return nil
			},
		},
		{
			name: "a negative duration is no ceiling, never a past deadline",
			in:   PoolPolicy{Enabled: true, MaxConns: 4, ReadTimeout: -time.Second, ReadWait: -time.Second},
			want: func(p PoolPolicy) error {
				if p.ReadTimeout != 0 || p.ReadWait != 0 {
					return errors.New("a negative budget survived")
				}
				return nil
			},
		},
		{
			name: "a pool of zero falls back to the shipped size",
			in:   PoolPolicy{Enabled: true},
			want: func(p PoolPolicy) error {
				if p.MaxConns != defaultMaxConns {
					return errors.New("the server would hold no connections")
				}
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.want(tc.in.normalize()); err != nil {
				t.Fatalf("%v: %+v", err, tc.in.normalize())
			}
		})
	}
}

// Rollback has to be total. With the guard off nothing this change added is
// in the path: no ceiling, no wait budget, no class gate.
func TestDisabledPolicyLeavesNothingBehind(t *testing.T) {
	pol := PoolPolicy{Enabled: false, MaxConns: 8, ReadTimeout: time.Second, ReadWait: time.Second}.normalize()
	for _, class := range []QueryClass{ClassBackground, ClassInteractive, ClassProbe} {
		if got := pol.statementTimeout(class); got != 0 {
			t.Errorf("class %s still has a %v ceiling with the guard off", class, got)
		}
		if got := pol.wait(class); got != 0 {
			t.Errorf("class %s still has a %v wait budget with the guard off", class, got)
		}
	}
	p := newConnPool(nil, pol)
	for _, class := range []QueryClass{ClassBackground, ClassInteractive, ClassProbe} {
		if gates := p.gatesFor(class); gates != nil {
			t.Errorf("class %s still passes %d admission gates with the guard off", class, len(gates))
		}
	}
	if cap(p.sem) != 8 {
		t.Errorf("the pool cap changed under the disabled guard: %d", cap(p.sem))
	}
}

// Work that never said what it was keeps the semantics it had before this
// existed. Every CLI call, every test and every background loop in the repo
// relies on that.
func TestUnclassifiedWorkIsBackgroundAndUnbounded(t *testing.T) {
	if got := QueryClassOf(context.Background()); got != ClassBackground {
		t.Fatalf("an unclassified context asks as %s", got)
	}
	pol := DefaultPoolPolicy().normalize()
	if got := pol.statementTimeout(ClassBackground); got != 0 {
		t.Fatalf("background work was given a %v ceiling it never asked for", got)
	}
	if got := pol.wait(ClassBackground); got != 0 {
		t.Fatalf("background work was given a %v wait budget it never asked for", got)
	}
}

func TestQueryBudgetSurvivesBeingNil(t *testing.T) {
	var b *QueryBudget
	if got := b.Class(); got != ClassBackground {
		t.Fatalf("a nil budget asks as %s", got)
	}
	if busy, timeouts, waited := b.Pressure(); busy != 0 || timeouts != 0 || waited != 0 {
		t.Fatalf("a nil budget reported pressure: %d %d %v", busy, timeouts, waited)
	}
	if got := WithQueryBudget(context.Background(), nil); got == nil {
		t.Fatal("attaching a nil budget produced a nil context")
	}
}

// A client that hangs up and a ceiling this code set share one SQLSTATE. An
// operator reading "query timeout" has to be reading about the ceiling.
func TestQueryTimeoutIsToldApartFromACancelledClient(t *testing.T) {
	ceiling := &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}
	client := &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"}
	other := &pgconn.PgError{Code: "42P01", Message: "relation does not exist"}

	if !IsQueryTimeout(ceiling) || !IsQueryCanceled(ceiling) {
		t.Error("a statement timeout was not recognized")
	}
	if IsQueryTimeout(client) {
		t.Error("a client cancel was reported as a statement timeout")
	}
	if !IsQueryCanceled(client) {
		t.Error("a client cancel was not recognized as a cancellation")
	}
	if IsQueryTimeout(other) || IsQueryCanceled(other) {
		t.Error("an ordinary SQL error was read as a cancellation")
	}
	// Wrapped is how every one of these actually arrives; a message that
	// merely quotes the text is not the same thing and must not match.
	if !IsQueryTimeout(fmt.Errorf("serverstore: listWanted: %w", ceiling)) {
		t.Error("a wrapped statement timeout was not recognized")
	}
	if IsQueryTimeout(errors.New("serverstore: listWanted: " + ceiling.Error())) {
		t.Error("an error that only quotes the text was matched")
	}
	if !IsPoolBusy(ErrPoolBusy) {
		t.Error("ErrPoolBusy is not recognized as itself")
	}
	if IsPoolBusy(context.DeadlineExceeded) {
		t.Error("a context deadline was read as saturation")
	}
}

func TestStatementTimeoutSetupNeverOutlivesItsCaller(t *testing.T) {
	caller, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	setup, stop := statementTimeoutSetupContext(caller)
	defer stop()

	callerDeadline, callerHasDeadline := caller.Deadline()
	setupDeadline, setupHasDeadline := setup.Deadline()
	if !callerHasDeadline || !setupHasDeadline {
		t.Fatal("caller and statement-timeout setup must both carry a deadline")
	}
	if setupDeadline.After(callerDeadline) {
		t.Fatalf("setup deadline %v outlives caller deadline %v", setupDeadline, callerDeadline)
	}
	<-setup.Done()
	if !errors.Is(setup.Err(), context.DeadlineExceeded) {
		t.Fatalf("setup ended with %v, want caller deadline exceeded", setup.Err())
	}
}

func TestStatementTimeoutSetupHasAFiveSecondUpperBoundWithoutACallerDeadline(t *testing.T) {
	setup, stop := statementTimeoutSetupContext(context.Background())
	defer stop()
	deadline, ok := setup.Deadline()
	if !ok {
		t.Fatal("statement-timeout setup has no independent upper bound")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 5*time.Second {
		t.Fatalf("statement-timeout setup budget = %v, want (0, 5s]", remaining)
	}
}

func TestPoolCountsOnlyStatementTimeoutsAsTimeoutPressure(t *testing.T) {
	p := newConnPool(nil, DefaultPoolPolicy())
	budget := NewQueryBudget(ClassInteractive)
	ctx := WithQueryBudget(context.Background(), budget)
	clientCancel := &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"}
	statementTimeout := &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}

	p.observeQueryError(ctx, clientCancel)
	if _, timeouts, _ := budget.Pressure(); timeouts != 0 {
		t.Fatalf("client cancellation counted as %d query timeout(s)", timeouts)
	}
	p.observeQueryError(ctx, statementTimeout)
	if _, timeouts, _ := budget.Pressure(); timeouts != 1 {
		t.Fatalf("statement timeout count = %d, want 1", timeouts)
	}
}

func TestInteractiveBudgetStopsAfterOnePoolRefusal(t *testing.T) {
	pol := DefaultPoolPolicy()
	pol.ReadWait = 15 * time.Millisecond
	p := newConnPool(nil, pol)
	for i := 0; i < cap(p.inter); i++ {
		p.inter <- struct{}{}
	}
	budget := NewQueryBudget(ClassInteractive)
	ctx := WithQueryBudget(context.Background(), budget)

	started := time.Now()
	for i := 0; i < 20; i++ {
		if _, err := p.acquire(ctx); !IsPoolBusy(err) {
			t.Fatalf("acquire %d error = %v, want ErrPoolBusy", i+1, err)
		}
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("twenty attempts spent %v, want only one bounded pool wait", elapsed)
	}

	stats := p.stat().Classes[0]
	if stats.Attempts != 20 || stats.First != 1 || stats.Followups != 19 {
		t.Fatalf("attempt split = total %d first %d followups %d, want 20/1/19",
			stats.Attempts, stats.First, stats.Followups)
	}
	if stats.Busy != 1 || stats.Suppressed != 19 || stats.Acquired != 0 {
		t.Fatalf("outcomes = acquired %d busy %d suppressed %d, want 0/1/19",
			stats.Acquired, stats.Busy, stats.Suppressed)
	}
	if stats.Attempts != stats.Acquired+stats.Busy+stats.Suppressed+stats.Canceled+stats.Failed {
		t.Fatalf("attempt outcomes do not close: %+v", stats)
	}
	if got := budget.Suppressed(); got != 19 {
		t.Fatalf("request suppressed count = %d, want 19", got)
	}
}

func TestPoolSeparatesFirstFollowupAndRetryAttempts(t *testing.T) {
	pol := DefaultPoolPolicy()
	pol.ProbeWait = time.Nanosecond
	p := newConnPool(nil, pol)
	for i := 0; i < cap(p.probe); i++ {
		p.probe <- struct{}{}
	}

	first := WithQueryBudget(context.Background(), NewQueryBudget(ClassProbe))
	if _, err := p.acquire(first); !IsPoolBusy(err) {
		t.Fatalf("first attempt = %v, want ErrPoolBusy", err)
	}
	retry := WithQueryBudget(context.Background(), NewRetryQueryBudget(ClassProbe))
	if _, err := p.acquire(retry); !IsPoolBusy(err) {
		t.Fatalf("retry attempt = %v, want ErrPoolBusy", err)
	}
	stats := p.stat().Classes[2]
	if stats.Attempts != 2 || stats.First != 1 || stats.Retries != 1 || stats.Followups != 0 {
		t.Fatalf("attempt accounting = %+v, want attempts=2 first=1 retries=1 followups=0", stats)
	}
}

func TestProbeAdmissionIsBoundedToItsReserve(t *testing.T) {
	p := newConnPool(nil, DefaultPoolPolicy())
	if got, want := cap(p.probe), p.pol.ProbeReserve; got != want {
		t.Fatalf("probe gate = %d, want reserve %d", got, want)
	}
	stats := p.stat().Classes[2]
	if stats.Limit != p.pol.ProbeReserve {
		t.Fatalf("reported probe limit = %d, want %d", stats.Limit, p.pol.ProbeReserve)
	}
}

func TestPoolPolicyFromEnvChangesOnlyWhatIsNamed(t *testing.T) {
	def := DefaultPoolPolicy().normalize()
	if got := PoolPolicyFromEnv(func(string) string { return "" }); got != def {
		t.Fatalf("an empty environment did not produce the shipped policy:\n got %+v\nwant %+v", got, def)
	}

	env := map[string]string{
		"CSX_DB_MAX_CONNS":     "12",
		"CSX_DB_READ_TIMEOUT":  "2500ms",
		"CSX_DB_READ_WAIT":     "0",
		"CSX_DB_PROBE_TIMEOUT": "1s",
	}
	got := PoolPolicyFromEnv(func(k string) string { return env[k] })
	if got.MaxConns != 12 || got.ReadTimeout != 2500*time.Millisecond || got.ReadWait != 0 || got.ProbeTimeout != time.Second {
		t.Fatalf("named settings were not applied: %+v", got)
	}
	if !got.Enabled || got.ProbeReserve != def.ProbeReserve {
		t.Fatalf("an unnamed setting moved: %+v", got)
	}

	off := map[string]string{"CSX_DB_POOL_GUARD": "OFF"}
	if got := PoolPolicyFromEnv(func(k string) string { return off[k] }); got.Enabled {
		t.Fatal("CSX_DB_POOL_GUARD=OFF did not turn the guard off")
	}

	// A typo must not take the server down at boot, and must not silently
	// become zero either -- zero here means "no ceiling at all".
	junk := map[string]string{"CSX_DB_READ_TIMEOUT": "8 seconds", "CSX_DB_MAX_CONNS": "lots"}
	got = PoolPolicyFromEnv(func(k string) string { return junk[k] })
	if got.ReadTimeout != def.ReadTimeout || got.MaxConns != def.MaxConns {
		t.Fatalf("an unparsable setting changed the policy: %+v", got)
	}
}
