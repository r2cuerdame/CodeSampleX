package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type busyShardStore struct {
	serverstore.Store
}

func (s *busyShardStore) GetShardEtag(context.Context, string) (string, bool, error) {
	return "", false, serverstore.ErrPoolBusy
}

func (s *busyShardStore) GetShard(context.Context, string) (string, string, bool, error) {
	return "", "", false, serverstore.ErrPoolBusy
}

func TestShardPoolBusyUsesRetryable429(t *testing.T) {
	busy := &busyShardStore{}
	srv, _, _ := newTestServer(t, func(d *Deps) {
		busy.Store = d.Store
		d.Store = busy
	})

	for _, etag := range []string{"", `"old"`} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/shards/npm/axios/1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("etag=%q status=%d, want 429", etag, resp.StatusCode)
		}
		if got := resp.Header.Get("Retry-After"); got != "2" {
			t.Fatalf("etag=%q Retry-After=%q, want 2", etag, got)
		}
	}
}
