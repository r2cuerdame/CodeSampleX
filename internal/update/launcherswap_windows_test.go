//go:build windows

package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTheLauncherSwapRefusesABinaryThatWillNotStart(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(exe, []byte("the launcher already installed"), 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := fileSHA256(exe)
	if err != nil {
		t.Fatal(err)
	}

	body := []byte("MZ this is not a program")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	replaced, err := c.replaceLauncherIfStale(context.Background(), root, Asset{
		OS: "windows", Arch: "amd64",
		LauncherURL: srv.URL, LauncherSize: int64(len(body)),
		LauncherSHA256: hex.EncodeToString(sum[:]),
	})
	if replaced {
		t.Error("a launcher that cannot start was installed")
	}
	if err == nil {
		t.Error("the failed swap reported success")
	} else if !strings.Contains(err.Error(), "self-test") {
		t.Errorf("error names something other than the self-test: %v", err)
	}
	after, err := fileSHA256(exe)
	if err != nil {
		t.Fatalf("the installed launcher is gone: %v", err)
	}
	if after != before {
		t.Error("the installed launcher was changed by a swap that failed")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "csx.exe" {
			t.Errorf("the failed swap left %s behind", e.Name())
		}
	}
}

func TestTheLauncherSwapRefusesBytesTheManifestDidNotSign(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(exe, []byte("the launcher already installed"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("some other launcher entirely")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	_, err := c.replaceLauncherIfStale(context.Background(), root, Asset{
		OS: "windows", Arch: "amd64",
		LauncherURL: srv.URL, LauncherSize: int64(len(body)),
		LauncherSHA256: strings.Repeat("a", 64),
	})
	if err == nil {
		t.Fatal("bytes that do not match the signed digest were accepted")
	}
	if !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("error names something other than the digest: %v", err)
	}
}

func TestTheLauncherSwapIsANoOpWhenAlreadyCurrent(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "csx.exe")
	installed := []byte("the launcher already installed")
	if err := os.WriteFile(exe, installed, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(installed)

	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	replaced, err := c.replaceLauncherIfStale(context.Background(), root, Asset{
		OS: "windows", Arch: "amd64",
		LauncherURL: srv.URL, LauncherSize: int64(len(installed)),
		LauncherSHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Error("an unchanged launcher was reported as replaced")
	}
	if asked {
		t.Error("the launcher was downloaded even though the installed one already matches")
	}
}

func TestAManifestWithoutALauncherChangesNothing(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(exe, []byte("the launcher already installed"), 0o700); err != nil {
		t.Fatal(err)
	}
	c := &Client{}
	replaced, err := c.replaceLauncherIfStale(context.Background(), root,
		Asset{OS: "windows", Arch: "amd64"})
	if err != nil || replaced {
		t.Errorf("replaced=%v err=%v, want a silent no-op", replaced, err)
	}
}

func TestASecondSwapSucceedsWhileTheFirstAsideIsStillHeld(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(exe, []byte("launcher one"), 0o700); err != nil {
		t.Fatal(err)
	}

	oldAside := exe + ".previous-1"
	if err := os.WriteFile(oldAside, []byte("launcher zero"), 0o700); err != nil {
		t.Fatal(err)
	}
	holder, err := os.Open(oldAside)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()

	staged := filepath.Join(root, "staged.exe")
	if err := os.WriteFile(staged, []byte("launcher two"), 0o700); err != nil {
		t.Fatal(err)
	}
	c := &Client{}
	if err := c.installLauncher(exe, staged); err != nil {
		t.Fatalf("a held earlier aside blocked the swap: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "launcher two" {
		t.Errorf("installed launcher is %q, want the newest", got)
	}
	if _, err := os.Stat(oldAside); err != nil {
		t.Errorf("the held aside was disturbed: %v", err)
	}
}

func TestTheLauncherSwapRestoresAMissingLauncher(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "csx.exe")
	if _, err := os.Stat(exe); !os.IsNotExist(err) {
		t.Fatalf("csx.exe unexpectedly exists: %v", err)
	}

	staged := filepath.Join(root, "staged.exe")
	if err := os.WriteFile(staged, []byte("restored launcher"), 0o700); err != nil {
		t.Fatal(err)
	}

	c := &Client{}
	if err := c.installLauncher(exe, staged); err != nil {
		t.Fatalf("failed to install missing launcher: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("installed launcher is missing: %v", err)
	}
	if string(got) != "restored launcher" {
		t.Errorf("installed launcher is %q, want restored launcher", got)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("staged binary was not consumed: %v", err)
	}
}

func TestTheLauncherSwapProceedsWhenLauncherIsMissing(t *testing.T) {
	root := t.TempDir()

	body := []byte("MZ not a runnable launcher")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	replaced, err := c.replaceLauncherIfStale(context.Background(), root, Asset{
		OS: "windows", Arch: "amd64",
		LauncherURL: srv.URL, LauncherSize: int64(len(body)),
		LauncherSHA256: hex.EncodeToString(sum[:]),
	})
	if replaced {
		t.Error("an unrunnable launcher was accepted")
	}
	if err == nil {
		t.Fatal("expected self-test failure, got nil")
	}
	if !strings.Contains(err.Error(), "self-test") {
		t.Errorf("expected self-test error, got %v", err)
	}
}

type bootstrapLauncherRoundTripper struct {
	stableManifest    []byte
	bootstrapManifest []byte
	payloadBinary     []byte
}

func (r bootstrapLauncherRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body := r.payloadBinary
	switch {
	case strings.Contains(req.URL.Path, "csx-update-stable.json"):
		body = r.stableManifest
	case strings.Contains(req.URL.Path, "csx-bootstrap-stable.json"):
		body = r.bootstrapManifest
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(string(body))),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

func TestCheckSyncsStaleLauncherFromBootstrapManifestEvenWhenPayloadIsCurrent(t *testing.T) {
	c, home, root, oldPath := launcherClientFixture(t)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	c.PublicKey = pub
	c.CurrentVersion = "v1.0.0"

	payloadBytes := []byte(fixtureInstalledPayload)
	payloadSum := sha256.Sum256(payloadBytes)
	base := "https://github.com/r2cuerdame/CodeSampleX/releases/download/v1.0.0/"
	payloadName := "csx-windows-" + runtime.GOARCH + ".exe"
	launcherName := "csx-launcher-windows-" + runtime.GOARCH + ".exe"

	stableManifest := Manifest{
		Schema:      1,
		Channel:     "stable",
		Sequence:    6,
		Version:     "v1.0.0",
		PublishedAt: now.Add(-time.Hour),
		ExpiresAt:   now.Add(24 * time.Hour),
		Assets: []Asset{{
			OS:                 "windows",
			Arch:               runtime.GOARCH,
			URL:                base + payloadName,
			Size:               int64(len(payloadBytes)),
			SHA256:             hex.EncodeToString(payloadSum[:]),
			MinLauncherVersion: "v1.0.0",
		}},
	}
	bootstrapManifest := stableManifest
	bootstrapManifest.Assets = append([]Asset(nil), stableManifest.Assets...)
	bootstrapManifest.Assets[0].LauncherURL = base + launcherName
	bootstrapManifest.Assets[0].LauncherSize = int64(len(payloadBytes))
	bootstrapManifest.Assets[0].LauncherSHA256 = hex.EncodeToString(payloadSum[:])

	signEnv := func(m Manifest) []byte {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		env, err := json.Marshal(Envelope{
			Payload:   base64.StdEncoding.EncodeToString(raw),
			Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw)),
		})
		if err != nil {
			t.Fatal(err)
		}
		return env
	}

	c.HTTP = &http.Client{Transport: bootstrapLauncherRoundTripper{
		stableManifest:    signEnv(stableManifest),
		bootstrapManifest: signEnv(bootstrapManifest),
		payloadBinary:     payloadBytes,
	}}
	if err := SaveState(home, State{Schema: 1, NextCheck: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if !c.Due() {
		t.Fatal("expected Due() to be true when LauncherVersion does not match CurrentVersion")
	}

	if err := os.WriteFile(filepath.Join(root, "csx.exe"), payloadBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := c.Check(context.Background(), true)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if res.LauncherError != "" {
		t.Fatalf("unexpected LauncherError: %s", res.LauncherError)
	}
	st, err := LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if st.LauncherVersion != "v1.0.0" {
		t.Fatalf("st.LauncherVersion = %q, want v1.0.0", st.LauncherVersion)
	}
	if c.Due() {
		t.Fatal("expected Due() to be false after LauncherVersion matches CurrentVersion")
	}
	_ = oldPath
}
