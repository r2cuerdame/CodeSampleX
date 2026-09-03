package daemon

// A sync request is not bound by the ordinary request timeout.
//
// The client gives every daemon call 30 seconds. Status, search, sample
// reads -- those are right at 30 seconds. A sync on the reporting
// workstation takes about fifteen minutes, so `csx sync` got "context
// deadline exceeded" at 30 seconds every time, and the CLI then ran the
// whole sync AGAIN in its own process, silently, while the daemon's copy
// carried on -- two syncs contending for a 246MB sqlite (Farm#18).
//
// Sync keeps the caller's context and nothing else: the CLI decides how
// long it is willing to wait, and the daemon keeps working while it waits.
// The ordinary timeout is a package variable so this test can shrink it
// instead of sleeping thirty seconds.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSyncOutlivesTheOrdinaryRequestTimeoutButStatusDoesNot(t *testing.T) {
	prev := requestTimeout
	requestTimeout = 150 * time.Millisecond
	defer func() { requestTimeout = prev }()

	delay := 400 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/local/v1/sync":
			_, _ = w.Write([]byte(`{"schemaVersion":1,"warmedKeys":7}`))
		default:
			_, _ = w.Write([]byte(`{"schemaVersion":1}`))
		}
	}))
	defer srv.Close()

	c := clientForTest(srv.URL)

	res, err := c.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync was cut off by the ordinary timeout: %v", err)
	}
	if res.WarmedKeys != 7 {
		t.Fatalf("Sync result = %+v", res)
	}

	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("Status outlived the ordinary timeout; only Sync may")
	}

	// The caller's own deadline still binds a sync.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Sync(ctx); err == nil {
		t.Fatal("Sync ignored the caller's deadline")
	}
}

// clientForTest is a Client over an httptest server, with no transport of
// its own so http() builds one bound by requestTimeout.
func clientForTest(base string) *Client { return &Client{BaseURL: base} }
