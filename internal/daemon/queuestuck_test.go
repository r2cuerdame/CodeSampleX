package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// A queue that is FIFO with a drain limit has a head, and anything stuck at
// the head is a wall. The drainer's rule was "4xx means this payload will
// never be accepted, so it stops being retried by attempt count" — nothing
// read the count, so a rejected item was re-POSTed on every tick forever.
// Once enough accumulated to fill the drain window, every valid report
// behind them stopped being sent, while `csx sync` kept exiting 0.
//
// Adoption reports are the only signal the network gets about whether its
// answers actually work, and the only one that cannot be recomputed from
// anything else.
func TestRejectedItemsDoNotWallOffTheQueue(t *testing.T) {
	ctx := context.Background()
	db := openQueueDB(t)

	// One more rejected item than a single drain pass can hold, so a
	// drainer that keeps retrying them can never reach what follows.
	const rejected = queueDrainLimit + 1
	for i := 0; i < rejected; i++ {
		if _, err := db.Enqueue(ctx, "adoption", `{"stale":true}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Enqueue(ctx, "adoption", `{"good":true}`); err != nil {
		t.Fatal(err)
	}

	var delivered int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		if string(buf[:n]) == `{"good":true}` {
			delivered++
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusBadRequest) // never going to be accepted
	}))
	defer srv.Close()

	d := &Daemon{
		DB:  db,
		Cfg: &config.Config{Mode: config.ModeCommunity, ServerURL: srv.URL},
	}

	// A handful of passes, as a daemon would run over minutes.
	for pass := 0; pass < 4 && delivered == 0; pass++ {
		if _, err := d.drainQueue(ctx); err != nil && pass == 0 {
			t.Logf("drain pass %d: %v", pass, err) // rejections are expected
		}
	}

	if delivered == 0 {
		t.Fatal("the deliverable report was never sent: the rejected items still wall off the queue")
	}
	n, err := db.QueueSetAsideCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != rejected {
		t.Errorf("set aside %d items, want %d — the count is what tells the user they exist", n, rejected)
	}
}

func openQueueDB(t *testing.T) *localdb.DB {
	t.Helper()
	db, err := localdb.Open(t.TempDir() + "/csx.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
