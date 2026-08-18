package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSampleWorkerRefreshUsesCompleteCLICommand(t *testing.T) {
	const token = "csx_author_v1_secret"
	expires := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/authoring/session/refresh" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("authorization = %q", got)
		}
		var body struct {
			ComputerName string `json:"computerName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ComputerName) == "" {
			t.Fatalf("refresh body = %+v, err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"idleExpiresAt":%q}`, expires.Format(time.RFC3339))
	}))
	defer srv.Close()

	oldClient, oldOut, oldErr := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr
	t.Cleanup(func() { sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = oldClient, oldOut, oldErr })
	sampleWorkerClient = srv.Client()
	var out, stderr bytes.Buffer
	sampleWorkerStdout, sampleWorkerStderr = &out, &stderr
	if code := sampleWorkerMain(context.Background(), []string{"refresh", "--server", srv.URL, "--token", token}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(out.String(), expires.Format(time.RFC3339)) {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestSampleWorkerRefreshFailsClosed(t *testing.T) {
	for _, server := range []string{"http://example.com", "https://user@example.com", "https://example.com/path"} {
		if _, err := sampleWorkerServerURL(server); err == nil {
			t.Errorf("accepted server %q", server)
		}
	}
}
