package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Client talks to a running daemon's local API, either over TCP
// (BaseURL) or over the platform IPC transport (NewIPCClient).
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// BaseURLFor resolves the daemon's TCP base URL for a home: the live
// daemon.addr file when a daemon is (or recently was) running — which
// also covers ephemeral ports — falling back to the configured port.
func BaseURLFor(home string) (string, error) {
	if raw, err := os.ReadFile(addrFile(home)); err == nil {
		if addr := strings.TrimSpace(string(raw)); addr != "" {
			return "http://" + addr, nil
		}
	}
	cfg, err := config.Load(home)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", cfg.DaemonPort), nil
}

// NewClient builds a TCP client for the daemon serving home.
func NewClient(home string) (*Client, error) {
	base, err := BaseURLFor(home)
	if err != nil {
		return nil, err
	}
	return &Client{BaseURL: base}, nil
}

// NewIPCClient builds a client over the Windows named pipe / unix socket
// for home. The host in BaseURL is cosmetic; the transport dials the pipe.
func NewIPCClient(home string) *Client {
	return &Client{
		BaseURL: "http://csx-daemon",
		HTTP:    &http.Client{Transport: ipcTransport(home), Timeout: 30 * time.Second},
	}
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// do performs one JSON round-trip. in==nil sends no body; out==nil
// discards the response body.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		msg := ""
		if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e) == nil {
			msg = e.Error
		}
		return fmt.Errorf("daemon: %s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if out == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) //nolint:errcheck
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Status fetches GET /local/v1/status.
func (c *Client) Status(ctx context.Context) (StatusInfo, error) {
	var st StatusInfo
	err := c.do(ctx, http.MethodGet, "/local/v1/status", nil, &st)
	return st, err
}

// Search runs POST /local/v1/search.
func (c *Client) Search(ctx context.Context, req domain.SearchRequest) (LocalSearchResponse, error) {
	var resp LocalSearchResponse
	err := c.do(ctx, http.MethodPost, "/local/v1/search", req, &resp)
	return resp, err
}

// Sample fetches GET /local/v1/samples/{id}.
func (c *Client) Sample(ctx context.Context, id string) (SampleInfo, error) {
	var info SampleInfo
	err := c.do(ctx, http.MethodGet, "/local/v1/samples/"+url.PathEscape(id), nil, &info)
	return info, err
}

// Adopt reports POST /local/v1/adoption.
func (c *Client) Adopt(ctx context.Context, req AdoptionRequest) error {
	return c.do(ctx, http.MethodPost, "/local/v1/adoption", req, nil)
}

// Queue fetches the privacy preview, GET /local/v1/queue.
func (c *Client) Queue(ctx context.Context) (QueuePreview, error) {
	var q QueuePreview
	err := c.do(ctx, http.MethodGet, "/local/v1/queue", nil, &q)
	return q, err
}

// Sync triggers POST /local/v1/sync (shard warm + upload now).
func (c *Client) Sync(ctx context.Context) (SyncResult, error) {
	var res SyncResult
	err := c.do(ctx, http.MethodPost, "/local/v1/sync", nil, &res)
	return res, err
}

// Stats fetches GET /local/v1/stats.
func (c *Client) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := c.do(ctx, http.MethodGet, "/local/v1/stats", nil, &st)
	return st, err
}

// Shutdown asks the daemon to stop gracefully.
func (c *Client) Shutdown(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/local/v1/shutdown", nil, nil)
}

// ensureTimeout bounds how long EnsureRunning waits for a spawned daemon.
const ensureTimeout = 10 * time.Second

// EnsureRunning returns a client for the daemon serving home, spawning
// "csx daemon run" detached (Windows: new process group + detached;
// Unix: setsid) when none answers. If expectedVersion is supplied, a live
// daemon from a different build is stopped gracefully first. That matters on
// upgrade: replacing the executable does not replace the already-running
// process, and accepting it here can leave new queue/protocol behavior dormant
// until the machine reboots.
//
// The spawned daemon inherits home via CSX_HOME and logs to
// $home/logs/daemon.log.
func EnsureRunning(ctx context.Context, home string, expectedVersion ...string) (*Client, error) {
	want := Version
	if len(expectedVersion) > 0 {
		want = expectedVersion[0]
	}
	return ensureRunning(ctx, home, want, func() error { return spawnDetached(home) })
}

// StopRunning gracefully stops the daemon for home and waits until both its
// listener and single-instance lock are gone. alreadyRunning is false when no
// daemon answered; that is a successful no-op for init/config transitions.
func StopRunning(ctx context.Context, home string) (alreadyRunning bool, err error) {
	c, _, err := probeRunning(ctx, home)
	if err != nil {
		if daemonLockHeld(home) {
			return true, fmt.Errorf("daemon is running but its status endpoint is unavailable: %w", err)
		}
		return false, nil
	}
	if err := stopAndWait(ctx, home, c); err != nil {
		return true, err
	}
	return true, nil
}

func ensureRunning(ctx context.Context, home, expectedVersion string, spawn func() error) (*Client, error) {
	if c, st, err := probeRunning(ctx, home); err == nil {
		if expectedVersion == "" || st.Version == expectedVersion {
			return c, nil
		}
		if err := stopAndWait(ctx, home, c); err != nil {
			return nil, fmt.Errorf("daemon: replace version %q with %q: %w", st.Version, expectedVersion, err)
		}
	}

	if err := spawn(); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(ensureTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if c, st, err := probeRunning(ctx, home); err == nil {
			if expectedVersion == "" || st.Version == expectedVersion {
				return c, nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, errors.New("daemon: spawned but not ready within 10s (see logs/daemon.log)")
}

func spawnDetached(home string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("daemon: locate csx binary: %w", err)
	}
	if stable, stableErr := csxupdate.StableExecutable(home, exe); stableErr == nil {
		exe = stable
	}
	cmd := exec.Command(exe, "daemon", "run")
	cmd.Env = append(os.Environ(), "CSX_HOME="+home)
	cmd.SysProcAttr = detachSysProcAttr()
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o700); err == nil {
		if logf, err := os.OpenFile(filepath.Join(home, "logs", "daemon.log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			cmd.Stdout, cmd.Stderr = logf, logf
			defer logf.Close()
		}
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("daemon: spawn: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}

func probeRunning(ctx context.Context, home string) (*Client, StatusInfo, error) {
	c, err := NewClient(home)
	if err != nil {
		return nil, StatusInfo{}, err
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := c.Status(pctx)
	if err != nil {
		return nil, StatusInfo{}, err
	}
	// A stale daemon.addr can point at a subsequently reused port. Never stop
	// or accept a process merely because it speaks the same small status JSON.
	if filepath.Clean(st.Home) != filepath.Clean(home) {
		return nil, StatusInfo{}, fmt.Errorf("daemon: status home %q does not match %q", st.Home, home)
	}
	return c, st, nil
}

func stopAndWait(ctx context.Context, home string, c *Client) error {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := c.Shutdown(sctx)
	cancel()
	if err != nil {
		// A daemon can finish between the status probe and shutdown request.
		if _, _, probeErr := probeRunning(ctx, home); probeErr != nil && !daemonLockHeld(home) {
			return nil
		}
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	deadline := time.Now().Add(ensureTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, _, probeErr := probeRunning(ctx, home)
		if probeErr != nil && !daemonLockHeld(home) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("daemon did not stop within 10s")
}

func daemonLockHeld(home string) bool {
	raw, err := os.ReadFile(filepath.Join(home, "daemon.lock"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	return err == nil && pidAlive(pid)
}
