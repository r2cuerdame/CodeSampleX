package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type blockingShardStore struct {
	serverstore.Store
	reads   atomic.Int64
	started chan struct{}
	release chan struct{}
	err     error
}

func TestShardMissCancellation(t *testing.T) {
	for _, cancelLeader := range []bool{false, true} {
		name := "waiter"
		if cancelLeader {
			name = "leader"
		}
		t.Run(name, func(t *testing.T) {
			a, fake, _, _ := newShardRefreshAPI(t)
			if err := fake.PutShard(context.Background(), "npm/zod/3", "v2", `{}`); err != nil {
				t.Fatal(err)
			}
			store := &blockingShardStore{Store: fake, started: make(chan struct{}, 10), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(store.release) }) }
			t.Cleanup(release)
			a.d.Store = store
			leader, stopLeader := context.WithCancel(context.Background())
			defer stopLeader()
			waiter, stopWaiter := context.WithCancel(context.Background())
			defer stopWaiter()
			waiting := &shardWaitContext{Context: waiter, waiting: make(chan struct{})}
			leaderResult, waiterResult := make(chan error, 1), make(chan error, 1)
			go func() { _, _, _, err := a.loadShard(leader, "npm/zod/3"); leaderResult <- err }()
			awaitShardSignal(t, store.started)
			go func() { _, _, _, err := a.loadShard(waiting, "npm/zod/3"); waiterResult <- err }()
			awaitShardSignal(t, waiting.waiting)
			canceled, live := waiterResult, leaderResult
			if cancelLeader {
				stopLeader()
				canceled, live = leaderResult, waiterResult
			} else {
				stopWaiter()
			}
			select {
			case err := <-canceled:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled read: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled request remained blocked")
			}
			if cancelLeader {
				awaitShardSignal(t, store.started)
			}
			release()
			select {
			case err := <-live:
				if err != nil {
					t.Fatalf("live request: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("live request did not complete")
			}
			want := int64(1)
			if cancelLeader {
				want = 2
			}
			if got := store.reads.Load(); got != want {
				t.Fatalf("reads = %d, want %d", got, want)
			}
		})
	}
}

func TestShardMissFailureAndAbsenceAreRetried(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "absent"
		if fail {
			name = "error"
		}
		t.Run(name, func(t *testing.T) {
			a, fake, _, _ := newShardRefreshAPI(t)
			store := &blockingShardStore{Store: fake, release: make(chan struct{})}
			close(store.release)
			if fail {
				store.err = errors.New("temporary read failure")
			}
			a.d.Store = store
			_, _, ok, err := a.loadShard(context.Background(), "npm/zod/3")
			if ok || (err != nil) != fail {
				t.Fatalf("first read: ok=%v err=%v", ok, err)
			}
			store.err = nil
			if err := fake.PutShard(context.Background(), "npm/zod/3", "v2", `{}`); err != nil {
				t.Fatal(err)
			}
			etag, doc, ok, err := a.loadShard(context.Background(), "npm/zod/3")
			if err != nil || !ok || etag != "v2" || doc != `{}` {
				t.Fatalf("retry: %q %q %v %v", etag, doc, ok, err)
			}
			if got := store.reads.Load(); got != 2 {
				t.Fatalf("reads = %d, want 2", got)
			}
		})
	}
}

func TestShardMissDifferentKeysReadIndependently(t *testing.T) {
	a, fake, _, _ := newShardRefreshAPI(t)
	store := &blockingShardStore{Store: fake, started: make(chan struct{}, 2), release: make(chan struct{})}
	a.d.Store = store
	var once sync.Once
	release := func() { once.Do(func() { close(store.release) }) }
	t.Cleanup(release)
	finished := make(chan struct{}, 2)
	for _, key := range []string{"npm/zod/3", "npm/axios/1"} {
		go func() { a.loadShard(context.Background(), key); finished <- struct{}{} }()
	}
	awaitShardSignal(t, store.started)
	awaitShardSignal(t, store.started)
	release()
	awaitShardSignal(t, finished)
	awaitShardSignal(t, finished)
}

type panickingShardStore struct {
	*blockingShardStore
	panics atomic.Bool
}

func (s *panickingShardStore) GetShard(ctx context.Context, key string) (string, string, bool, error) {
	etag, doc, ok, err := s.blockingShardStore.GetShard(ctx, key)
	if s.panics.CompareAndSwap(true, false) {
		panic("store read panicked")
	}
	return etag, doc, ok, err
}

func TestShardMissLeaderPanicReleasesWaiters(t *testing.T) {
	a, fake, _, _ := newShardRefreshAPI(t)
	if err := fake.PutShard(context.Background(), "npm/zod/3", "v2", `{}`); err != nil {
		t.Fatal(err)
	}
	store := &panickingShardStore{blockingShardStore: &blockingShardStore{Store: fake, started: make(chan struct{}, 10), release: make(chan struct{})}}
	store.panics.Store(true)
	var once sync.Once
	release := func() { once.Do(func() { close(store.release) }) }
	t.Cleanup(release)
	a.d.Store = store
	leaderPanicked := make(chan any, 1)
	go func() {
		defer func() { leaderPanicked <- recover() }()
		a.loadShard(context.Background(), "npm/zod/3")
	}()
	awaitShardSignal(t, store.started)
	waiting := &shardWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	type result struct {
		etag string
		ok   bool
		err  error
	}
	waiterResult := make(chan result, 1)
	go func() {
		etag, _, ok, err := a.loadShard(waiting, "npm/zod/3")
		waiterResult <- result{etag, ok, err}
	}()
	awaitShardSignal(t, waiting.waiting)
	release()
	if p := <-leaderPanicked; p == nil {
		t.Fatal("leader read did not panic")
	}
	select {
	case r := <-waiterResult:
		if r.err != nil || !r.ok || r.etag != "v2" {
			t.Fatalf("waiter after leader panic: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter blocked behind a panicked read")
	}
	if got := store.reads.Load(); got != 2 {
		t.Fatalf("reads = %d, want 2", got)
	}
}

func (s *blockingShardStore) GetShard(ctx context.Context, key string) (string, string, bool, error) {
	s.reads.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return "", "", false, ctx.Err()
	case <-s.release:
	}
	if s.err != nil {
		return "", "", false, s.err
	}
	return s.Store.GetShard(ctx, key)
}

// Signals when the handler has reached its cancellable wait (or, before the
// fix, a second store read), without depending on scheduler sleeps.
type shardWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *shardWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func shardRequest(a *api, ctx context.Context) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/shards/npm/zod/3", nil).WithContext(ctx)
	r.SetPathValue("ecosystem", "npm")
	r.SetPathValue("rest", "zod/3")
	w := httptest.NewRecorder()
	a.handleShard(w, r)
	return w
}

func awaitShardSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("shard request did not reach its wait")
	}
}

func TestShardConcurrentMissesShareRead(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "cold"
		if expired {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			a, fake, _, now := newShardRefreshAPI(t)
			if err := fake.PutShard(context.Background(), "npm/zod/3", "v2", `{"new":true}`); err != nil {
				t.Fatal(err)
			}
			if expired {
				a.rememberShard("npm/zod/3", "v1", `{"old":true}`, now.Add(-time.Minute))
			}
			store := &blockingShardStore{Store: fake, started: make(chan struct{}, 10), release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(store.release) }) }
			t.Cleanup(release)
			a.d.Store = store
			results := make(chan *httptest.ResponseRecorder, 5)
			go func() { results <- shardRequest(a, context.Background()) }()
			awaitShardSignal(t, store.started)
			for i := 0; i < 4; i++ {
				ctx := &shardWaitContext{Context: context.Background(), waiting: make(chan struct{})}
				go func() { results <- shardRequest(a, ctx) }()
				awaitShardSignal(t, ctx.waiting)
			}
			reads := store.reads.Load()
			release()
			for i := 0; i < 5; i++ {
				select {
				case w := <-results:
					if w.Code != 200 || w.Header().Get("ETag") != `"v2"` || w.Body.String() != `{"new":true}` {
						t.Errorf("response: %d %s %s", w.Code, w.Header().Get("ETag"), w.Body.String())
					}
				case <-time.After(5 * time.Second):
					t.Fatal("request did not finish")
				}
			}
			if reads != 1 {
				t.Fatalf("concurrent full reads = %d, want 1", reads)
			}
		})
	}
}
