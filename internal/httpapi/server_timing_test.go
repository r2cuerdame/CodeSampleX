package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
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

// #716: the probe measured /v1/stats and /version at 0.000 s in-process and
// every second outside it. What an outer middleware does after the handler
// returns (csx-server's pressure log and stdout request record) must not hold
// back a response that is already complete.
func TestTimedRoutesAnswerBeforeOuterMiddlewareReturns(t *testing.T) {
	answered := make(chan struct{})
	var deps Deps
	srv, _, _ := newTestServer(t, func(d *Deps) { deps = *d })
	srv.Close()
	mux := NewMux(deps)
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		select {
		case <-answered:
		case <-time.After(5 * time.Second):
		}
	}))
	defer outer.Close()
	defer close(answered)
	// A fresh connection per request, like the probe: the previous request's
	// connection is still inside the blocked outer middleware.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	var getStatsLen int64
	for _, tc := range []struct {
		method     string
		path       string
		wantStatus int
	}{
		{http.MethodGet, "/version", http.StatusOK},
		{http.MethodGet, "/v1/stats", http.StatusOK},
		{http.MethodHead, "/v1/stats", http.StatusOK},
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/readyz", http.StatusOK},
		{http.MethodGet, "/v1/shards/npm/axios/1", http.StatusNotFound},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			done := make(chan error, 1)
			var resp *http.Response
			go func() {
				req, err := http.NewRequest(tc.method, outer.URL+tc.path, nil)
				if err != nil {
					done <- err
					return
				}
				resp, err = client.Do(req)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s %s: no response while the outer middleware was still running", tc.method, tc.path)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.wantStatus || len(resp.TransferEncoding) != 0 {
				t.Fatalf("%s %s: status %d, transfer-encoding %v; want unchunked %d", tc.method, tc.path, resp.StatusCode, resp.TransferEncoding, tc.wantStatus)
			}
			if tc.method == http.MethodHead {
				if len(body) != 0 {
					t.Fatalf("HEAD %s: got %d body bytes; want empty body", tc.path, len(body))
				}
				if resp.ContentLength != getStatsLen {
					t.Fatalf("HEAD %s: Content-Length %d, want %d", tc.path, resp.ContentLength, getStatsLen)
				}
			} else {
				if resp.ContentLength != int64(len(body)) {
					t.Fatalf("%s: Content-Length %d, body %d bytes", tc.path, resp.ContentLength, len(body))
				}
				if tc.path == "/v1/stats" {
					getStatsLen = resp.ContentLength
				}
			}
			assertServerTiming(t, resp.Header.Get("Server-Timing"))
		})
	}
}

func TestTimedRespectsBodylessStatusesAndTransferEncoding(t *testing.T) {
	a := &api{}
	for _, tc := range []struct {
		name               string
		method             string
		status             int
		setTransferChunked bool
		writeBody          string
		wantLengthHeader   string
		wantBody           string
	}{
		{
			name:             "304_no_content_length_or_body",
			method:           http.MethodGet,
			status:           http.StatusNotModified,
			wantLengthHeader: "",
			wantBody:         "",
		},
		{
			name:             "204_no_content_length_or_body",
			method:           http.MethodGet,
			status:           http.StatusNoContent,
			wantLengthHeader: "",
			wantBody:         "",
		},
		{
			name:             "head_keeps_content_length_without_body",
			method:           http.MethodHead,
			status:           http.StatusOK,
			writeBody:        `{"ok":true}`,
			wantLengthHeader: "11",
			wantBody:         "",
		},
		{
			name:               "preset_transfer_encoding_skips_content_length",
			method:             http.MethodGet,
			status:             http.StatusOK,
			setTransferChunked: true,
			writeBody:          "hello",
			wantLengthHeader:   "",
			wantBody:           "hello",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler := a.timed(func(w http.ResponseWriter, _ *http.Request) {
				if tc.setTransferChunked {
					w.Header().Set("Transfer-Encoding", "chunked")
				}
				w.WriteHeader(tc.status)
				if tc.writeBody != "" {
					_, _ = w.Write([]byte(tc.writeBody))
				}
			})
			handler(rec, httptest.NewRequest(tc.method, "/v1/stats", nil))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := rec.Header().Get("Content-Length"); got != tc.wantLengthHeader {
				t.Fatalf("Content-Length = %q, want %q", got, tc.wantLengthHeader)
			}
			if got := rec.Body.String(); got != tc.wantBody {
				t.Fatalf("body = %q, want %q", got, tc.wantBody)
			}
			assertServerTiming(t, rec.Header().Get("Server-Timing"))
		})
	}
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
