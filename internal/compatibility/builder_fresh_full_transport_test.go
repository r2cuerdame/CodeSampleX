package compatibility

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type freshFullTransportStore struct {
	*serverstore.Fake
	fail    bool
	failure error
	cancel  context.CancelFunc
	writes  []string
}

type freshFullSocketTimeout struct{}

func (freshFullSocketTimeout) Error() string   { return "i/o timeout" }
func (freshFullSocketTimeout) Timeout() bool   { return true }
func (freshFullSocketTimeout) Temporary() bool { return true }

func (s *freshFullTransportStore) PutShard(ctx context.Context, key, etag, raw string) error {
	s.writes = append(s.writes, key)
	if s.fail && strings.HasPrefix(key, "npm/charlie/") {
		if s.cancel != nil {
			s.cancel()
		}
		if s.failure != nil {
			return s.failure
		}
		return &net.OpError{Op: "read", Net: "tcp", Err: freshFullSocketTimeout{}}
	}
	return s.Fake.PutShard(ctx, key, etag, raw)
}

func TestFreshFullTransportFailureRetainsCommittedCursor(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%t", restart), func(t *testing.T) {
			now := testNow
			clock := func() time.Time { return now }
			fake := serverstore.NewFake()
			fake.NowFn = clock
			seedRepairCorpus(t, fake)
			last := now.Add(-time.Minute)
			writeStaleStats(t, fake, last)
			store := &freshFullTransportStore{Fake: fake, fail: true}
			first := &Builder{
				Store: store, Now: clock, repairChunk: 2,
				lastRun: last, lastCompletedAt: last,
				fullRepairAt: now, passes: 1, statusLoaded: true,
			}
			err := first.RunOnce(context.Background())
			var networkError net.Error
			if !errors.As(err, &networkError) || !networkError.Timeout() {
				t.Fatalf("full error = %v, want a wrapped TCP timeout", err)
			}
			st := readBuilderStatus(t, fake)
			if st.LastPassOutcome == PassOutcomeSuccess || !st.LastPassFull {
				t.Fatalf("failed full was reported as completed: %+v", st)
			}
			if st.Repair == nil || st.Repair.Cursor != "npm/axios" || st.Repair.PackagesDone != 2 {
				t.Fatalf("TCP failure lost committed first chunk: %+v", st.Repair)
			}
			if got := generatedAtOf(t, fake); got != last.Format(time.RFC3339) {
				t.Fatalf("failed full moved stats to %s", got)
			}
			if first.repairCache != nil {
				t.Fatal("failed full retained its corpus cache")
			}

			store.fail = false
			store.writes = nil
			now = now.Add(5 * time.Minute)
			next := first
			if restart {
				next = &Builder{Store: store, Now: clock}
			}
			if err := next.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, key := range store.writes {
				if strings.HasPrefix(key, "npm/alpha/") || strings.HasPrefix(key, "npm/axios/") {
					t.Fatalf("retry rewrote already committed first chunk: %s", key)
				}
			}
			for _, name := range []string{"bravo", "charlie", "delta", "echo"} {
				found := false
				for _, key := range store.writes {
					found = found || strings.HasPrefix(key, "npm/"+name+"/")
				}
				if !found {
					t.Fatalf("retry skipped unfinished package %s", name)
				}
			}
			st = readBuilderStatus(t, fake)
			if st.LastPassOutcome != PassOutcomeSuccess || !st.LastPassFull ||
				st.Repair != nil || st.ConsecutiveFailures != 0 {
				t.Fatalf("retry did not actually complete full: %+v", st)
			}
			if got := generatedAtOf(t, fake); got != testNow.Format(time.RFC3339) {
				t.Fatalf("resumed full changed its start stamp: %s", got)
			}
			// Independent uninterrupted materializer: compare actual snapshots
			// and shards rather than only the progress bookkeeping.
			oracle := serverstore.NewFake()
			oracle.NowFn = func() time.Time { return testNow }
			seedRepairCorpus(t, oracle)
			oracle.NowFn = clock
			whole := &Builder{Store: oracle, Now: func() time.Time { return testNow }, repairChunk: 2}
			if err := whole.runRepair(context.Background(), testNow, 0, time.Now()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(readOutputs(t, fake), readOutputs(t, oracle)) {
				t.Fatal("resumed TCP failure changed exhaustive shard or snapshot outputs")
			}
		})
	}
}

func TestFreshFullNonTransportFailureKeepsExistingFallback(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err          error
		cancelCaller bool
	}{
		{name: "permanent", err: errors.New("persistent shard failure")},
		{name: "pool-busy", err: serverstore.ErrPoolBusy},
		{name: "statement-timeout", err: &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "cancelled-caller-tcp-timeout", cancelCaller: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := serverstore.NewFake()
			fake.NowFn = func() time.Time { return testNow }
			seedRepairCorpus(t, fake)
			last := testNow.Add(-time.Minute)
			writeStaleStats(t, fake, last)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &freshFullTransportStore{Fake: fake, fail: true, failure: tc.err}
			if tc.cancelCaller {
				store.cancel = cancel
			}
			b := &Builder{
				Store: store, Now: func() time.Time { return testNow }, repairChunk: 2,
				lastRun: last, lastCompletedAt: last,
				fullRepairAt: testNow, passes: 1, statusLoaded: true,
			}
			if err := b.RunOnce(ctx); err == nil {
				t.Fatal("failed full reported success")
			}
			st := readBuilderStatus(t, fake)
			if st.Repair != nil || b.repair != nil || b.repairCache != nil {
				t.Fatalf("nontransport failure retained mandatory unfinished work: %+v", st.Repair)
			}
			if st.LastPassOutcome == PassOutcomeSuccess ||
				generatedAtOf(t, fake) != last.Format(time.RFC3339) {
				t.Fatal("failed full advanced its completion or stats clock")
			}
		})
	}
}
