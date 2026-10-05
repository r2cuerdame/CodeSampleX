package httpapi

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestDiagnosticRoutesExposeServerTiming(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	if err := store.PutShard(context.Background(), "npm/axios/1", "deadbeef", `{"schemaVersion":1,"key":"npm/axios/1","packages":[]}`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/healthz", http.StatusOK},
		{"/version", http.StatusOK},
		{"/v1/stats", http.StatusOK},
		{"/v1/shards/npm/axios/1", http.StatusOK},
		{"/v1/shards/npm/absent/1", http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer secret-test-token")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			assertServerTiming(t, resp.Header.Get("Server-Timing"))
		})
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/shards/npm/axios/1", nil)
	req.Header.Set("If-None-Match", `"deadbeef"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation status = %d", resp.StatusCode)
	}
	assertServerTiming(t, resp.Header.Get("Server-Timing"))
}

func assertServerTiming(t *testing.T, header string) {
	t.Helper()
	for _, forbidden := range []string{"secret-test-token", "axios", "/v1/", "SELECT"} {
		if strings.Contains(header, forbidden) {
			t.Fatalf("Server-Timing exposed %q: %q", forbidden, header)
		}
	}
	fields := strings.Split(header, ", ")
	if len(fields) != 4 {
		t.Fatalf("Server-Timing = %q, want four phases", header)
	}
	for i, name := range []string{"middleware", "db_wait", "query_handler", "serialize"} {
		value, ok := strings.CutPrefix(fields[i], name+";dur=")
		if !ok {
			t.Fatalf("Server-Timing phase %d = %q, want %s", i, fields[i], name)
		}
		ms, err := strconv.ParseFloat(value, 64)
		if err != nil || ms < 0 {
			t.Fatalf("Server-Timing duration %q is invalid: %v", value, err)
		}
	}
}
