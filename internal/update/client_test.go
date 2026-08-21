package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type blockingRoundTripper struct {
	inner   http.RoundTripper
	started chan struct{}
	release chan struct{}
}

func (r blockingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	close(r.started)
	<-r.release
	return r.inner.RoundTrip(req)
}

type updateRoundTripper struct{ manifest, binary []byte }

func (r updateRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body := r.binary
	if strings.Contains(req.URL.Path, "update-stable") {
		body = r.manifest
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), ContentLength: int64(len(body)), Request: req}, nil
}

func clientFixture(t *testing.T, binary []byte, sequence uint64) (*Client, string, string) {
	t.Helper()
	clearLauncherEnvironment(t)
	home, dir := t.TempDir(), t.TempDir()
	exe := filepath.Join(dir, "csx")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := AdoptStandalone(home, exe); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	sum := sha256.Sum256(binary)
	name := "csx-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	m := Manifest{Schema: 1, Channel: "stable", Sequence: sequence, Version: "v1.1.0", PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Assets: []Asset{{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: "https://github.com/r2cuerdame/CodeSampleX/releases/download/v1.1.0/" + name, Size: int64(len(binary)), SHA256: hex.EncodeToString(sum[:])}}}
	payload, _ := json.Marshal(m)
	env, _ := json.Marshal(Envelope{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))})
	c := &Client{Home: home, CurrentVersion: "v1.0.0", Executable: exe, ManifestURL: "https://test.invalid/csx-update-stable.json", PublicKey: pub, HTTP: &http.Client{Transport: updateRoundTripper{manifest: env, binary: binary}}, Now: func() time.Time { return now }, SelfTest: func(context.Context, string, string) error { return nil }, ValidateTarget: func(string) error { return nil }, CheckApplySupport: func() error { return nil }, directApplyForTests: true}
	c.Replace = func(current, staged, previous string) error {
		raw, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		if err := os.WriteFile(previous, raw, 0o700); err != nil {
			return err
		}
		return os.Rename(staged, current)
	}
	return c, home, exe
}

// Tests may themselves be launched through the installed Windows csx
// launcher (for example `csx run -- go test ...`). Those process-wide values
// describe the real installation and must never be mistaken for a fixture's
// synthetic payload. Launcher-specific tests set their own values after this
// helper returns.
func clearLauncherEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"CSX_LAUNCHER_ROOT", "CSX_LAUNCHER_PATH", "CSX_LAUNCHER_VERSION",
		"CSX_PAYLOAD_VERSION", "CSX_ACTIVE_SEQUENCE", "CSX_ACTIVE_SHA256",
	} {
		t.Setenv(name, "")
	}
}

func TestClientAppliesVerifiedBinaryAndPreservesPrevious(t *testing.T) {
	c, home, exe := clientFixture(t, []byte("new-binary"), 7)
	res, err := c.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || !res.RestartRequired {
		t.Fatalf("result=%+v", res)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new-binary" {
		t.Fatalf("current=%q", got)
	}
	if got, _ := os.ReadFile(exe + ".previous"); string(got) != "old" {
		t.Fatalf("previous=%q", got)
	}
	st, _ := LoadState(home)
	if st.HighestSequence != 7 || st.HighestVersion != "v1.1.0" || st.PendingRestart != "v1.1.0" {
		t.Fatalf("state=%+v", st)
	}
}

func TestClientRejectsReplayBeforeDownload(t *testing.T) {
	c, home, exe := clientFixture(t, []byte("new"), 7)
	if err := SaveState(home, State{Schema: 1, HighestSequence: 8, HighestVersion: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Check(context.Background(), true); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("error=%v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("old binary changed: %q", got)
	}
}

func TestClientHashMismatchLeavesOldBinary(t *testing.T) {
	c, _, exe := clientFixture(t, []byte("signed"), 7)
	c.HTTP = &http.Client{Transport: updateRoundTripper{manifest: c.HTTP.Transport.(updateRoundTripper).manifest, binary: []byte("tampered")}}
	if _, err := c.Check(context.Background(), true); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("old binary changed: %q", got)
	}
}

func TestUnsupportedApplyIsSuccessfulCheckWithManualInstallNotice(t *testing.T) {
	c, home, exe := clientFixture(t, []byte("new"), 7)
	c.CheckApplySupport = func() error { return errors.New("cannot replace running executable") }
	res, err := c.Check(context.Background(), true)
	if err != nil || !res.Available || !res.ManualInstallRequired || res.Applied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("binary changed: %q", got)
	}
	st, err := LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if st.ConsecutiveFailures != 0 || st.LastError != "" || st.NextCheck.IsZero() {
		t.Fatalf("manual install requirement recorded as failure: %+v", st)
	}
}

func TestClientStreamsOversizedBodyAndCleansStage(t *testing.T) {
	c, _, exe := clientFixture(t, []byte("signed"), 7)
	c.HTTP = &http.Client{Transport: updateRoundTripper{manifest: c.HTTP.Transport.(updateRoundTripper).manifest, binary: []byte("signed-extra")}}
	if _, err := c.Check(context.Background(), true); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("oversized streamed body error=%v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(exe), ".csx-update-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("staged download leaked: %v, %v", matches, err)
	}
}

func TestAppliedCommitSurvivesPostCommitFailureAndRetry(t *testing.T) {
	c, _, exe := clientFixture(t, []byte("new-binary"), 7)
	c.SyncDir = func(string) error { return errors.New("injected sync failure") }
	res, err := c.Check(context.Background(), true)
	if err == nil || !res.Applied || !res.RestartRequired {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got, _ := os.ReadFile(exe + ".previous"); string(got) != "old" {
		t.Fatalf("previous=%q", got)
	}
	c.SyncDir = nil
	res, err = c.Check(context.Background(), true)
	if err != nil || !res.Applied {
		t.Fatalf("retry res=%+v err=%v", res, err)
	}
	if got, _ := os.ReadFile(exe + ".previous"); string(got) != "old" {
		t.Fatalf("retry overwrote previous=%q", got)
	}
}

func TestStateLockSerializesCheckAndActivation(t *testing.T) {
	c, home, _ := clientFixture(t, []byte("new"), 8)
	if err := SaveState(home, State{Schema: 1, HighestSequence: 2, HighestVersion: "v1.0.0", PendingRestart: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	c.HTTP = &http.Client{Transport: blockingRoundTripper{inner: c.HTTP.Transport, started: started, release: release}}
	checkDone := make(chan error, 1)
	go func() { _, err := c.Check(context.Background(), false); checkDone <- err }()
	<-started
	ackDone := make(chan error, 1)
	go func() { ackDone <- AcknowledgeActivation(home, "v1.0.0") }()
	select {
	case err := <-ackDone:
		t.Fatalf("activation bypassed active update lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-checkDone; err != nil {
		t.Fatal(err)
	}
	if err := <-ackDone; err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if st.HighestSequence != 8 || st.PendingRestart != "" {
		t.Fatalf("state clobbered: %+v", st)
	}
}

func TestConcurrentClientsPreserveHighestAntiReplayState(t *testing.T) {
	high, home, _ := clientFixture(t, []byte("new"), 8)
	low, _, _ := clientFixture(t, []byte("new"), 7)
	low.Home = home
	started, release := make(chan struct{}), make(chan struct{})
	high.HTTP = &http.Client{Transport: blockingRoundTripper{inner: high.HTTP.Transport, started: started, release: release}}
	highDone := make(chan error, 1)
	lowDone := make(chan error, 1)
	go func() { _, err := high.Check(context.Background(), false); highDone <- err }()
	<-started
	go func() { _, err := low.Check(context.Background(), false); lowDone <- err }()
	time.Sleep(100 * time.Millisecond)
	close(release)
	if err := <-highDone; err != nil {
		t.Fatal(err)
	}
	if err := <-lowDone; err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("lower concurrent manifest error=%v", err)
	}
	st, err := LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if st.HighestSequence != 8 {
		t.Fatalf("anti-replay state clobbered: %+v", st)
	}
}

func TestLockNeverDeletesAnExistingStaleLookingOwner(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(updateDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(updateDir(home), "update.lock")
	// A pid that is genuinely alive. This used to say 1, which is init on
	// Unix and does not exist at all on Windows — so once the lock learned to
	// notice a dead owner, the fixture described a lock nobody held and the
	// test asserted we must not reclaim it. What it means to assert is that
	// age alone never overrules a LIVE owner: an update on a slow link holds
	// this for a long time, and trampling it would corrupt the thing the lock
	// exists to protect.
	owner := fmt.Sprintf("live-owner %d\n", os.Getpid())
	if err := os.WriteFile(path, []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := acquireLock(home, time.Now()); err == nil {
		t.Fatal("existing lock was broken")
	}
	if time.Since(start) < 4*time.Second {
		t.Fatal("lock failed without waiting for its owner")
	}
	if got, _ := os.ReadFile(path); string(got) != owner {
		t.Fatalf("owner lock changed: %q", got)
	}
}

func TestAcknowledgeActivationOnlyClearsMatchingRestart(t *testing.T) {
	home := t.TempDir()
	base := State{Schema: 1, HighestSequence: 42, HighestVersion: "v1.2.0", PendingRestart: "v1.2.0", PreviousPath: "kept"}
	if err := SaveState(home, base); err != nil {
		t.Fatal(err)
	}
	if err := AcknowledgeActivation(home, "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(home)
	if st.PendingRestart != "v1.2.0" || st.HighestSequence != 42 {
		t.Fatalf("nonmatching activation changed state: %+v", st)
	}
	if err := AcknowledgeActivation(home, "v1.2.0"); err != nil {
		t.Fatal(err)
	}
	st, _ = LoadState(home)
	if st.PendingRestart != "" || st.PreviousPath != "kept" || st.HighestSequence != 42 {
		t.Fatalf("matching activation lost state: %+v", st)
	}
	st.PendingRestart = "v1.1.0"
	if err := SaveState(home, st); err != nil {
		t.Fatal(err)
	}
	if err := AcknowledgeActivation(home, "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	st, _ = LoadState(home)
	if st.PendingRestart != "" {
		t.Fatalf("rollback activation not acknowledged: %+v", st)
	}
}

func TestRollbackPreviousValidationFailsClosed(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := validateRollbackBinary(missing); err == nil {
		t.Fatal("missing previous accepted")
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt")
	if err := os.WriteFile(corrupt, []byte("not executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateRollbackBinary(corrupt); err == nil {
		t.Fatal("corrupt previous accepted")
	}
	if runtime.GOOS != "windows" {
		dir := t.TempDir()
		target := filepath.Join(dir, "csx")
		if err := os.WriteFile(target, []byte("#!/bin/sh\necho 'csx v1.2.0'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := validateRollbackBinary(target); err != nil {
			t.Fatalf("valid previous rejected: %v", err)
		}
	}
}

func TestUpdatePolicyAndTrustedURLs(t *testing.T) {
	if !AutoEnabled("community", "auto") || AutoEnabled("local-only", "auto") || AutoEnabled("local-only", "on") || AutoEnabled("community", "off") {
		t.Fatal("auto update consent boundary is wrong")
	}
	if err := validateManifestURL(DefaultManifestURL, "stable"); err != nil {
		t.Fatal(err)
	}
	if err := validateDownloadURL(mustURL(t, "https://release-assets.githubusercontent.com/github-production-release-asset/x?sig=y")); err != nil {
		t.Fatalf("GitHub release redirect refused: %v", err)
	}
	for _, raw := range []string{"http://github.com/x", "https://user@github.com/x", "https://github.com:444/x", "https://evil.example/x"} {
		if err := validateDownloadURL(mustURL(t, raw)); err == nil {
			t.Errorf("trusted unsafe URL %s", raw)
		}
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
