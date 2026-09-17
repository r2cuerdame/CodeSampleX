package autoupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

type fakeUpdateTransport struct{ manifest, binary []byte }

func (f fakeUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := f.binary
	if strings.Contains(req.URL.Path, "update-stable") {
		body = f.manifest
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), ContentLength: int64(len(body)), Request: req}, nil
}

// Two long-running processes (an MCP-shaped loop and a daemon-shaped loop,
// contract #457) point at the same home and the same signed manifest at
// once. Neither may corrupt update/state.json or apply two different
// versions; the existing update.lock file serialization (proven directly
// against Client in internal/update/client_test.go's
// TestConcurrentClientsPreserveHighestAntiReplayState) must make this safe
// end to end through the shared Loop wrapper too.
func TestConcurrentLoopsOnTheSameHomeDoNotRace(t *testing.T) {
	if runtime.GOOS == "windows" {
		// update.Client.Check only allows apply=true on Windows through the
		// stable-launcher install path (replaceExecutable is unconditionally
		// disabled there); direct standalone replacement, exercised here, is
		// the Linux/macOS path. The Windows launcher path is exercised by
		// internal/update/launcher_windows_test.go and
		// internal/update/launcherswap_windows_test.go, and is unmodified by
		// this change.
		t.Skip("standalone direct-replace apply path is Linux/macOS only; see internal/update's launcher_windows_test.go for the Windows path")
	}
	for _, name := range []string{
		"CSX_LAUNCHER_ROOT", "CSX_LAUNCHER_PATH", "CSX_LAUNCHER_VERSION",
		"CSX_PAYLOAD_VERSION", "CSX_ACTIVE_SEQUENCE", "CSX_ACTIVE_SHA256",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "csx")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := csxupdate.AdoptStandalone(home, exe); err != nil {
		t.Fatal(err)
	}

	binary := []byte("new-binary")
	pub, priv, _ := ed25519.GenerateKey(nil)
	sum := sha256.Sum256(binary)
	name := "csx-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	now := time.Now().UTC()
	m := csxupdate.Manifest{Schema: 1, Channel: "stable", Sequence: 1, Version: "v9.9.9", PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Assets: []csxupdate.Asset{{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: "https://github.com/r2cuerdame/CodeSampleX/releases/download/v9.9.9/" + name, Size: int64(len(binary)), SHA256: hex.EncodeToString(sum[:])}}}
	payload, _ := json.Marshal(m)
	env, _ := json.Marshal(csxupdate.Envelope{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))})
	transport := fakeUpdateTransport{manifest: env, binary: binary}

	// Loop only calls RunCheck when client.Due() is true, and a fresh state
	// (no prior check) is due for both loops — but the instant either one's
	// Check() completes, it stamps update/state.json's NextCheck several
	// hours out, and Due() reads that fresh, so the other would never
	// become due again during this test. A barrier forces both loops to be
	// genuinely inside RunCheck, about to contend for update.lock, before
	// either is allowed to call Client.Check — the same overlap
	// internal/update/client_test.go's TestConcurrentClientsPreserveHighestAntiReplayState
	// forces with a blocking http.RoundTripper, adapted here one layer up
	// since two independent Loop() instances each own their own *Client.
	var barrierMu sync.Mutex
	arrivals := 0
	proceed := make(chan struct{})
	oldCheck := RunCheck
	RunCheck = func(ctx context.Context, client *csxupdate.Client) (csxupdate.Result, error) {
		client.PublicKey = pub
		client.ManifestURL = "https://test.invalid/csx-update-stable.json"
		client.HTTP = &http.Client{Transport: transport}
		client.SelfTest = func(context.Context, string, string) error { return nil }
		client.ValidateTarget = func(string) error { return nil }
		client.CheckApplySupport = func() error { return nil }
		client.Replace = func(current, staged, previous string) error {
			raw, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			if err := os.WriteFile(previous, raw, 0o700); err != nil {
				return err
			}
			return os.Rename(staged, current)
		}
		barrierMu.Lock()
		arrivals++
		if arrivals >= 2 {
			close(proceed)
		}
		barrierMu.Unlock()
		select {
		case <-proceed:
		case <-time.After(2 * time.Second):
			t.Error("only one loop ever reached RunCheck; the barrier never released")
		}
		return client.Check(ctx, true)
	}
	t.Cleanup(func() { RunCheck = oldCheck })

	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.AutoUpdate = "auto"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	mcpShaped := Loop(ctx, home, cfg, exe, "v1.0.0")
	daemonShaped := Loop(ctx, home, cfg, exe, "v1.0.0")

	drain := func(ch <-chan Outcome) []Outcome {
		var got []Outcome
		for o := range ch {
			got = append(got, o)
		}
		return got
	}
	a := drain(mcpShaped)
	b := drain(daemonShaped)

	// The winner applies for real. The loser, once it gets the lock, sees
	// state already at the target version/sequence and (on this Linux/macOS
	// standalone path) the executable's digest already matching the signed
	// asset — Client.Check's documented idempotent-retry behavior — so it
	// may report Applied too without downloading or replacing anything a
	// second time. Either outcome is safe; what must never happen is a
	// replay refusal below the now-current sequence, a corrupted binary, or
	// inconsistent state.
	applied := 0
	for _, o := range append(a, b...) {
		if o.Err != nil {
			t.Fatalf("unexpected error: %v", o.Err)
		}
		if o.Result.Applied {
			applied++
		}
	}
	if applied == 0 {
		t.Fatalf("neither loop applied the update (a=%+v b=%+v)", a, b)
	}
	if got, err := os.ReadFile(exe); err != nil || string(got) != string(binary) {
		t.Fatalf("executable ended up corrupted: %q, err=%v", got, err)
	}
	st, err := csxupdate.LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if st.HighestVersion != "v9.9.9" || st.PendingRestart != "v9.9.9" {
		t.Fatalf("state corrupted by the race: %+v", st)
	}
}
