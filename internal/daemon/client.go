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
	"syscall"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

var requestTimeout = 30 * time.Second

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

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
	if cfg.DaemonPort != 0 && cfg.DaemonPort != config.Default().DaemonPort {
		return fmt.Sprintf("http://127.0.0.1:%d", cfg.DaemonPort), nil
	}
	return "", fmt.Errorf("daemon: no address published for home %s", home)
}

func NewClient(home string) (*Client, error) {
	if base, err := BaseURLFor(home); err == nil {
		return &Client{BaseURL: base}, nil
	}
	return NewIPCClient(home), nil
}

func NewIPCClient(home string) *Client {
	return &Client{
		BaseURL: "http://csx-daemon",
		HTTP:    &http.Client{Transport: ipcTransport(home), Timeout: requestTimeout},
	}
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: requestTimeout}
}

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

func (c *Client) Status(ctx context.Context) (StatusInfo, error) {
	var st StatusInfo
	err := c.do(ctx, http.MethodGet, "/local/v1/status", nil, &st)
	return st, err
}

func (c *Client) Search(ctx context.Context, req domain.SearchRequest) (LocalSearchResponse, error) {
	var resp LocalSearchResponse
	err := c.do(ctx, http.MethodPost, "/local/v1/search", req, &resp)
	return resp, err
}

func (c *Client) Sample(ctx context.Context, id string) (SampleInfo, error) {
	var info SampleInfo
	err := c.do(ctx, http.MethodGet, "/local/v1/samples/"+url.PathEscape(id), nil, &info)
	return info, err
}

func (c *Client) Adopt(ctx context.Context, req AdoptionRequest) error {
	return c.do(ctx, http.MethodPost, "/local/v1/adoption", req, nil)
}

func (c *Client) Queue(ctx context.Context) (QueuePreview, error) {
	var q QueuePreview
	err := c.do(ctx, http.MethodGet, "/local/v1/queue", nil, &q)
	return q, err
}

func (c *Client) Sync(ctx context.Context) (SyncResult, error) {
	return c.sync(ctx, "/local/v1/sync")
}

func (c *Client) SyncUploads(ctx context.Context) (SyncResult, error) {
	return c.sync(ctx, "/local/v1/sync?uploadsOnly=1")
}

func (c *Client) sync(ctx context.Context, path string) (SyncResult, error) {
	var res SyncResult
	long := *c.http()
	long.Timeout = 0
	saved := c.HTTP
	c.HTTP = &long
	err := c.do(ctx, http.MethodPost, path, nil, &res)
	c.HTTP = saved
	return res, err
}

func (c *Client) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := c.do(ctx, http.MethodGet, "/local/v1/stats", nil, &st)
	return st, err
}

func (c *Client) Shutdown(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/local/v1/shutdown", nil, nil)
}

const ensureTimeout = 10 * time.Second

func EnsureRunning(ctx context.Context, home string, expectedVersion ...string) (*Client, error) {
	want := Version
	if len(expectedVersion) > 0 {
		want = expectedVersion[0]
	}
	return ensureRunning(ctx, home, want, func() error { return spawnDetached(home) })
}

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
		time.Sleep(50 * time.Millisecond)
	}
	return nil, errors.New("daemon: spawned but not ready within 10s (see logs/daemon.log)")
}

func spawnDetached(home string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("daemon: locate csx binary: %w", err)
	}
	if isTestBinary(exe) {
		return fmt.Errorf("daemon: refusing to spawn a test binary as a daemon (%s)", filepath.Base(exe))
	}
	if stable, stableErr := csxupdate.StableExecutable(home, exe); stableErr == nil {
		exe = stable
	}
	sealInheritedDescriptors()
	var logf *os.File
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o700); err == nil {
		if f, err := os.OpenFile(filepath.Join(home, "logs", "daemon.log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			logf = f
			defer logf.Close()
		}
	}
	start := func(attr *syscall.SysProcAttr) error {
		cmd := exec.Command(exe, "daemon", "run")
		cmd.Env = append(os.Environ(), "CSX_HOME="+home)
		cmd.SysProcAttr = attr
		if logf != nil {
			cmd.Stdout, cmd.Stderr = logf, logf
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		_ = cmd.Process.Release()
		return nil
	}
	if err := start(detachSysProcAttr()); err != nil {
		if fallback := detachFallbackSysProcAttr(); fallback != nil {
			if retryErr := start(fallback); retryErr == nil {
				return nil
			}
		}
		return fmt.Errorf("daemon: spawn: %w", err)
	}
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
	if filepath.Clean(st.Home) != filepath.Clean(home) {
		return nil, StatusInfo{}, fmt.Errorf("daemon: status home %q does not match %q", st.Home, home)
	}
	return c, st, nil
}

func stopAndWait(ctx context.Context, home string, c *Client) error {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	shutErr := c.Shutdown(sctx)
	cancel()

	deadline := time.Now().Add(ensureTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, _, probeErr := probeRunning(ctx, home)
		if probeErr != nil && !daemonLockHeld(home) {
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if shutErr != nil {
		return fmt.Errorf("graceful shutdown: %w", shutErr)
	}
	return errors.New("daemon did not stop within 10s")
}

func daemonLockHeld(home string) bool {
	raw, err := os.ReadFile(filepath.Join(home, "daemon.lock"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid == os.Getpid() {
		return false
	}
	return pidAlive(pid)
}

func isTestBinary(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	base = strings.TrimSuffix(base, ".exe")
	if strings.HasSuffix(base, ".test") {
		return true
	}
	return strings.Contains(strings.ToLower(filepath.ToSlash(path)), "/go-build")
}
