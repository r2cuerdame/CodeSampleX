package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/mcp"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func TestMCPStartupReturnsToolResponseThenDrainsPreexistingOutboxes(t *testing.T) {
	requests := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requests <- r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/evidence/batches":
			var body struct {
				Batches []json.RawMessage `json:"batches"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
		case "/v1/wanted/batches", "/v1/adoptions":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	home := newCLIHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = server.URL
	})
	seedMCPStartupBacklog(t, home)

	type runningDaemon struct {
		d      *daemon.Daemon
		client *daemon.Client
		cancel context.CancelFunc
		done   <-chan error
	}
	ensureEntered := make(chan struct{})
	releaseEnsure := make(chan struct{})
	daemonReady := make(chan runningDaemon, 1)
	var enterOnce sync.Once
	ensure := func(ctx context.Context, gotHome string, _ ...string) (*daemon.Client, error) {
		enterOnce.Do(func() { close(ensureEntered) })
		select {
		case <-releaseEnsure:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		d, err := daemon.New(gotHome)
		if err != nil {
			return nil, err
		}
		// The queued Wanted coordinate is already public in this closed test;
		// keep the regression hermetic by replacing the registry boundary.
		d.WantedPublic = func(context.Context, domain.PURL) bool { return true }
		runCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- d.Run(runCtx) }()
		select {
		case <-d.Ready():
			client := &daemon.Client{BaseURL: d.BaseURL()}
			daemonReady <- runningDaemon{d: d, client: client, cancel: cancel, done: done}
			return client, nil
		case err := <-done:
			cancel()
			_ = d.Close()
			return nil, fmt.Errorf("daemon exited before ready: %w", err)
		case <-ctx.Done():
			cancel()
			<-done
			_ = d.Close()
			return nil, ctx.Err()
		}
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	serveDone := make(chan int, 1)
	go func() {
		code := serveMCP(context.Background(), home, inR, outW, io.Discard, mcp.NewDeps, ensure)
		_ = outW.Close()
		serveDone <- code
	}()
	defer inW.Close()

	select {
	case <-ensureEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("community MCP startup never launched daemon ensure")
	}
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_local_stats","arguments":{}}}` + "\n"
	if _, err := io.WriteString(inW, request); err != nil {
		t.Fatal(err)
	}
	response := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(outR).ReadString('\n')
		response <- line
	}()
	select {
	case line := <-response:
		if !strings.Contains(line, `"id":1`) || !strings.Contains(line, `"result"`) {
			t.Fatalf("tool response = %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP tool response blocked on background daemon startup")
	}
	close(releaseEnsure)

	var running runningDaemon
	select {
	case running = <-daemonReady:
	case <-time.After(5 * time.Second):
		t.Fatal("background daemon did not become ready")
	}
	wantPaths := map[string]bool{
		"/v1/evidence/batches": false,
		"/v1/wanted/batches":   false,
		"/v1/adoptions":        false,
	}
	queueDeadline := time.NewTimer(4 * time.Second)
	for !wantPaths["/v1/wanted/batches"] || !wantPaths["/v1/adoptions"] {
		select {
		case path := <-requests:
			if _, tracked := wantPaths[path]; tracked {
				wantPaths[path] = true
			}
		case <-queueDeadline.C:
			t.Fatalf("MCP-started daemon missed the bounded feedback/Wanted first drain: %v", wantPaths)
		}
	}
	queueDeadline.Stop()
	evidenceDeadline := time.NewTimer(5 * time.Second)
	defer evidenceDeadline.Stop()
	for !wantPaths["/v1/evidence/batches"] {
		select {
		case path := <-requests:
			if _, tracked := wantPaths[path]; tracked {
				wantPaths[path] = true
			}
		case <-evidenceDeadline.C:
			t.Fatalf("MCP-started daemon missed the bounded evidence first upload: %v", wantPaths)
		}
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	if err := running.client.Shutdown(shutdownCtx); err != nil {
		cancelShutdown()
		t.Fatalf("bounded shutdown request: %v", err)
	}
	cancelShutdown()
	select {
	case err := <-running.done:
		if err != nil {
			t.Fatalf("daemon shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP-started daemon did not stop within the shutdown bound")
	}
	running.cancel()
	if err := running.d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := inW.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-serveDone:
		if code != 0 {
			t.Fatalf("serveMCP exit code = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP server did not stop after bounded input close")
	}

	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if pending, err := db.PendingObservations(t.Context(), 10); err != nil || len(pending) != 0 {
		t.Fatalf("evidence backlog after daemon drain = %d, err=%v", len(pending), err)
	}
	if pending, err := db.QueuePending(t.Context(), 10); err != nil || len(pending) != 0 {
		t.Fatalf("feedback/Wanted outbox after daemon drain = %d, err=%v", len(pending), err)
	}
}

func TestMCPAutostartIsCommunityOnly(t *testing.T) {
	for _, mode := range []string{config.ModeLocalOnly, config.ModeUninitialized} {
		t.Run(fmt.Sprintf("mode_%q", mode), func(t *testing.T) {
			home := newCLIHome(t, func(cfg *config.Config) { cfg.Mode = mode })
			var ensureCalls atomic.Int64
			ensure := func(context.Context, string, ...string) (*daemon.Client, error) {
				ensureCalls.Add(1)
				return nil, fmt.Errorf("must not be called")
			}
			var out bytes.Buffer
			in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_local_stats","arguments":{}}}` + "\n")
			if code := serveMCP(t.Context(), home, in, &out, io.Discard, mcp.NewDeps, ensure); code != 0 {
				t.Fatalf("serveMCP exit code = %d", code)
			}
			if ensureCalls.Load() != 0 {
				t.Fatalf("mode %q launched %d daemon ensure(s)", mode, ensureCalls.Load())
			}
			if !strings.Contains(out.String(), `"id":1`) || !strings.Contains(out.String(), `"result"`) {
				t.Fatalf("mode %q tool response = %q", mode, out.String())
			}
		})
	}
}

func seedMCPStartupBacklog(t *testing.T, home string) {
	t.Helper()
	ident, err := identity.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "24", Language: "javascript",
	}.Normalize()
	if err := db.SaveEnvironment(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordObservation(t.Context(), localdb.ObsKey{
		Epoch: "2026-08-18", PURL: "pkg:npm/axios@1.12.0", Symbol: "axios.post",
		EnvHash: env.Hash(), Stage: domain.StageUsed, Result: domain.ResultPass,
	}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Enqueue(t.Context(), "adoption",
		`{"schemaVersion":1,"evidenceClass":"ADOPTION_EVIDENCE","epoch":"2026-08-18","anonId":"anon","sampleId":"sha256:`+strings.Repeat("a", 64)+`","applied":true}`); err != nil {
		t.Fatal(err)
	}
	wanted, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"epoch":         "2026-08-18",
		"anonId":        ident.AnonID("2026-08-18"),
		"packages":      []string{"pkg:npm/axios@1.12.0"},
		"symbols":       []string{"axios.post"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Enqueue(t.Context(), "wanted", string(wanted)); err != nil {
		t.Fatal(err)
	}
}
