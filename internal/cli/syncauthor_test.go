package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

func TestAuthorSessionSyncSkipsCacheWarm(t *testing.T) {
	for _, tc := range []struct {
		name, token, mode    string
		args                 []string
		wantUpload, wantCall bool
	}{
		{"ordinary full sync", "", config.ModeCommunity, nil, false, true},
		{"author bare sync", "synthetic-author-token", config.ModeCommunity, nil, true, true},
		{"author explicit uploads", "synthetic-author-token", config.ModeCommunity, []string{"--uploads-only"}, true, true},
		{"ordinary explicit uploads", "", config.ModeCommunity, []string{"--uploads-only"}, true, true},
		{"author local only", "synthetic-author-token", config.ModeLocalOnly, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(sampleWorkerSessionTokenEnv, tc.token)
			var calls atomic.Int64
			var upload atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/local/v1/sync" {
					calls.Add(1)
					upload.Store(r.URL.Query().Get("uploadsOnly") == "1")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{})
			}))
			defer srv.Close()
			_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
			port, _ := strconv.Atoi(p)
			syncTestHome(t, tc.mode, port)
			if rc := syncMain(context.Background(), tc.args); rc != 0 {
				t.Fatalf("sync exited %d", rc)
			}
			if (calls.Load() == 1) != tc.wantCall || upload.Load() != tc.wantUpload {
				t.Fatalf("calls=%d uploadsOnly=%t; want called=%t uploadsOnly=%t", calls.Load(), upload.Load(), tc.wantCall, tc.wantUpload)
			}
		})
	}
}

func TestAuthorSessionSyncCancellationDoesNotSucceedOrRetry(t *testing.T) {
	t.Setenv(sampleWorkerSessionTokenEnv, "synthetic-author-token")
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/local/v1/sync" {
			calls.Add(1)
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	syncTestHome(t, config.ModeCommunity, port)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if rc := syncMain(ctx, nil); rc != 1 {
		t.Fatalf("cancelled sync exited %d, want 1", rc)
	}
	if calls.Load() != 1 {
		t.Fatalf("sync calls=%d, want exactly one", calls.Load())
	}
}
