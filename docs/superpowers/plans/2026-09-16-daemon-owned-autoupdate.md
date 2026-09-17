# Daemon-Owned Automatic Update Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give a normal first-party standalone **community** install a working automatic-update path even when the user never starts `csx mcp` or `csx worker start` — the already-running background sync daemon must own a bounded, signed update check — without duplicating the trust boundaries or racing MCP/worker on `update.lock`.

**Architecture:** Extract the existing `automaticUpdates` polling loop out of `internal/cli` (where it is currently private to the MCP/worker call sites) into a new, dependency-light `internal/autoupdate` package that both `internal/cli` and `internal/daemon` can import without creating an import cycle (`internal/config` already imports `internal/update`, so the loop cannot live in either of those packages). Wire `internal/daemon`'s existing background maintenance loop (`startBackground`) to run the same shared loop against the daemon's own `home`/`cfg`/executable. Reuse `internal/update.Client` completely unchanged — it already serializes concurrent callers on `update.lock`, already refuses non-owned/local-only/off installs, and already has the Windows-launcher/Linux-replace semantics fully covered by its own test suite. Surface a pending-restart notice on `csx daemon status` from the same shared `update/state.json` that `csx update status` already reads, so a daemon-only install has an explicit, visible signal instead of a silent one.

**Tech Stack:** Go 1.26 (see go.mod), stdlib `net/http`/`crypto/ed25519`, existing internal packages (`internal/config`, `internal/update`, `internal/daemon`, `internal/cli`).

**Spec:** GitHub issue #457 ("P0: community CLI/daemon installs do not run the promised automatic updater").

## Global Constraints

- Preserve every existing trust boundary unchanged: signed stable manifest, ownership marker (`OwnsExecutable`), OS/arch binding, signature/hash/size verification, persisted jitter/backoff, `update.lock` serialization. Do this by **not modifying `internal/update` at all** — only add a new caller.
- `local-only` and `autoUpdate=off` installs must make no automatic update network request, from any process, including the daemon. This is enforced by `csxupdate.AutoEnabled` inside the shared loop and must never be bypassed or duplicated at the call site.
- MCP and worker automatic-update behavior must not change (`internal/cli/mcp.go`, `internal/cli/worker.go` keep calling `automaticUpdates(...)` with the same signature).
- No duplicate updater *race*: concurrent daemon + MCP/worker checks must still serialize safely through the existing `update.lock` file (already proven by `internal/update/client_test.go`'s `TestConcurrentClientsPreserveHighestAntiReplayState` / `TestStateLockSerializesCheckAndActivation` — do not re-implement that locking).
- This is a minimal, hotfix-ready change: do not touch production deploy/release scope, do not touch the v0.1.197 runtime-isolation milestone, do not add a new always-on process.

---

## Task 1: Reproduce the daemon-only stale-update gap with a failing test

**Files:**
- Test: `internal/daemon/autoupdate_test.go` (new)

**Interfaces:**
- Consumes: `daemon.New`, `daemon.Daemon.startBackground` (existing, unexported, same-package test), `config.Default`/`config.ModeCommunity`, `internal/update.AdoptStandalone`.
- Produces: nothing consumed by later tasks — this is the regression guard.

- [ ] **Step 1: Write the failing reproduction test**

```go
package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

// This is the exact shape of GitHub issue #457: a community install whose
// only long-running process is the background sync daemon must still reach
// the signed update endpoint on a bounded cadence. Before this change,
// startBackground never called into the updater at all — MCP and the
// contributor worker were the only production call sites.
func TestDaemonOnlyInstallRunsTheAutomaticUpdateLoop(t *testing.T) {
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeCommunity
		c.AutoUpdate = "auto"
	})
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

	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.Executable = exe

	var checks atomic.Int32
	restoreInterval, restoreCheck := stubAutomaticUpdateForTest(t, 20*time.Millisecond, func() {
		checks.Add(1)
	})
	defer restoreInterval()
	defer restoreCheck()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startBackground(ctx)

	deadline := time.After(2 * time.Second)
	for checks.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("daemon-only background loop never reached the automatic update check")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
```

  `stubAutomaticUpdateForTest` does not exist yet; it is a small helper added
  in Task 3 once `internal/autoupdate` exists (it overrides
  `autoupdate.PollInterval` / `autoupdate.RunCheck`). Until Task 3 lands this
  test will not compile — that is expected and is itself part of the
  reproduction: today there is no seam in `internal/daemon` to hook at all.

- [ ] **Step 2: Confirm the reproduction**

  After Task 3 provides `stubAutomaticUpdateForTest` (compiles), run:

  `go test ./internal/daemon/ -run TestDaemonOnlyInstallRunsTheAutomaticUpdateLoop -v`

  Expected on pre-fix code (before Task 4 wires `startBackground`): **FAIL**
  with "daemon-only background loop never reached the automatic update
  check". This is the recorded reproduction of the issue.

## Task 2: Extract the shared automatic-update loop into `internal/autoupdate`

**Files:**
- Create: `internal/autoupdate/loop.go`
- Create: `internal/autoupdate/loop_test.go` (moved/adapted from `internal/cli/update_test.go`)
- Modify: `internal/cli/update.go`
- Modify: `internal/cli/update_test.go` (remove the moved test, keep `TestConsentSaveWaitsForAdmittedUpdateTransaction`)

**Interfaces:**
- Produces: `autoupdate.Outcome{Result update.Result; Err error}`, `autoupdate.PollInterval time.Duration` (var), `autoupdate.RunCheck func(context.Context, *update.Client) (update.Result, error)` (var), `autoupdate.Loop(ctx context.Context, home string, cfg *config.Config, exe, currentVersion string) <-chan Outcome`.
- Consumes (unchanged): `internal/config.Config`, `internal/config.Load`, `internal/update.AutoEnabled/OwnsExecutable/AcknowledgeActivation/ErrPolicyDisabled/Client`.

- [ ] **Step 1: Create `internal/autoupdate/loop.go`**

```go
// Package autoupdate holds the automatic signed-update polling loop shared
// by every long-running csx process: the stdio MCP server, the contributor
// worker, and the background sync daemon. It is deliberately its own
// package rather than living in internal/cli or internal/daemon: it needs
// both internal/config and internal/update, and internal/config already
// imports internal/update, so it cannot live in either without a cycle —
// and internal/daemon must be able to call it without importing
// internal/cli, which imports internal/daemon.
package autoupdate

import (
	"context"
	"errors"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

// Outcome is one result the loop reports: either an update.Result or an
// error from the check itself.
type Outcome struct {
	Result csxupdate.Result
	Err    error
}

// PollInterval is how often the loop wakes to ask client.Due(). A var so
// tests can shrink it; production leaves it at the default.
var PollInterval = 10 * time.Minute

// RunCheck performs one update check. A var so a test can replace the
// network call with a stub while still exercising the loop's control flow
// (consent reload, stop-on-Applied, stop-on-ErrPolicyDisabled).
var RunCheck = func(ctx context.Context, client *csxupdate.Client) (csxupdate.Result, error) {
	return client.Check(ctx, true)
}

// Loop contacts the release endpoint only with explicit community consent
// (or autoUpdate=on) and only for a first-party standalone install that
// owns exe. It reloads config on every iteration so a consent revocation
// mid-loop is honored before the next network request, and it stops after
// an applied update (the caller decides what "restart required" means for
// its own process).
func Loop(ctx context.Context, home string, cfg *config.Config, exe, currentVersion string) <-chan Outcome {
	out := make(chan Outcome, 1)
	go func() {
		defer close(out)
		_ = csxupdate.AcknowledgeActivation(home, currentVersion)
		if cfg == nil || !csxupdate.AutoEnabled(cfg.Mode, cfg.AutoUpdate) {
			return
		}
		owned, err := csxupdate.OwnsExecutable(home, exe)
		if err != nil || !owned {
			return
		}
		client := &csxupdate.Client{Home: home, CurrentVersion: currentVersion, Executable: exe, Channel: cfg.UpdateChannel, Automatic: true}
		client.Preflight = func() error {
			currentCfg, err := config.Load(home)
			if err != nil {
				return err
			}
			if !csxupdate.AutoEnabled(currentCfg.Mode, currentCfg.AutoUpdate) {
				return csxupdate.ErrPolicyDisabled
			}
			client.Channel = currentCfg.UpdateChannel
			return nil
		}
		for {
			currentCfg, loadErr := config.Load(home)
			if loadErr != nil {
				out <- Outcome{Err: loadErr}
				return
			}
			if !csxupdate.AutoEnabled(currentCfg.Mode, currentCfg.AutoUpdate) {
				return
			}
			client.Channel = currentCfg.UpdateChannel
			if client.Due() {
				res, err := RunCheck(ctx, client)
				if errors.Is(err, csxupdate.ErrPolicyDisabled) {
					return
				}
				out <- Outcome{Result: res, Err: err}
				if res.Applied {
					return
				}
			}
			t := time.NewTimer(PollInterval)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}()
	return out
}
```

- [ ] **Step 2: Move the consent-reload test into `internal/autoupdate/loop_test.go`**

```go
package autoupdate

import (
	"context"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

func TestLoopReloadsRevokedConsentBeforeNetwork(t *testing.T) {
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
	if err := csxupdate.AdoptStandalone(home, exe); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.AutoUpdate = "auto"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	if err := csxupdate.SaveState(home, csxupdate.State{Schema: 1, NextCheck: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	oldInterval, oldCheck := PollInterval, RunCheck
	PollInterval = 50 * time.Millisecond
	var checks atomic.Int32
	RunCheck = func(context.Context, *csxupdate.Client) (csxupdate.Result, error) {
		checks.Add(1)
		return csxupdate.Result{}, nil
	}
	t.Cleanup(func() { PollInterval, RunCheck = oldInterval, oldCheck })

	out := Loop(context.Background(), home, cfg, exe, "v1.0.0")
	time.Sleep(10 * time.Millisecond)
	cfg.AutoUpdate = "off"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("automatic update loop did not stop after consent revocation")
	}
	if got := checks.Load(); got != 0 {
		t.Fatalf("network check ran after consent revocation: %d", got)
	}
}
```

- [ ] **Step 3: Run the new package test**

  `go test ./internal/autoupdate/... -v` — expect PASS.

- [ ] **Step 4: Rewire `internal/cli/update.go`**

  Replace the whole `automaticUpdatePollInterval` var, `runAutomaticUpdateCheck`
  var, and `automaticUpdates` var/func body with:

```go
type automaticUpdateResult = autoupdate.Outcome

// automaticUpdates is a test seam shared by the worker and stdio MCP. The
// production loop contacts the release endpoint only with explicit
// community consent (or autoUpdate=on) and only for a first-party
// standalone install; the shared implementation lives in
// internal/autoupdate so internal/daemon can run the identical loop for a
// daemon-only install without importing internal/cli.
var automaticUpdates = func(ctx context.Context, home string, cfg *config.Config, exe string) <-chan automaticUpdateResult {
	return autoupdate.Loop(ctx, home, cfg, exe, Version)
}
```

  Add `"github.com/r2cuerdame/codesamplex/internal/autoupdate"` to the
  import block. Remove the now-unused `"errors"` import if nothing else in
  the file uses it (check with `goimports`/`go build`).

- [ ] **Step 5: Trim `internal/cli/update_test.go`**

  Delete `TestAutomaticUpdateReloadsRevokedConsentBeforeNetwork` (moved to
  Step 2) and its now-unused imports (`path/filepath`, `runtime`,
  `sync/atomic`), keeping `TestConsentSaveWaitsForAdmittedUpdateTransaction`
  untouched.

- [ ] **Step 6: Run the cli package tests**

  `go test ./internal/cli/... -run 'TestAutomaticUpdate|TestConsentSave' -v`
  — expect PASS (or "no tests to run" for the removed name, which is
  correct).

- [ ] **Step 7: Commit**

```bash
git add internal/autoupdate internal/cli/update.go internal/cli/update_test.go
git commit -m "refactor(cli): extract the automatic-update loop into internal/autoupdate"
```

## Task 3: Add the daemon-side test seam and finish Task 1's reproduction

**Files:**
- Modify: `internal/daemon/autoupdate_test.go` (from Task 1 — add the missing helper)

**Interfaces:**
- Consumes: `autoupdate.PollInterval`, `autoupdate.RunCheck`, `autoupdate.Outcome`, `csxupdate.Result`.
- Produces: `stubAutomaticUpdateForTest(t *testing.T, interval time.Duration, onCheck func()) (restoreInterval, restoreCheck func())` used by every test in `internal/daemon/autoupdate_test.go`.

- [ ] **Step 1: Add the stub helper to the same test file**

```go
func stubAutomaticUpdateForTest(t *testing.T, interval time.Duration, onCheck func()) (restoreInterval, restoreCheck func()) {
	t.Helper()
	oldInterval, oldCheck := autoupdate.PollInterval, autoupdate.RunCheck
	autoupdate.PollInterval = interval
	autoupdate.RunCheck = func(context.Context, *csxupdate.Client) (csxupdate.Result, error) {
		onCheck()
		return csxupdate.Result{}, nil
	}
	return func() { autoupdate.PollInterval = oldInterval },
		func() { autoupdate.RunCheck = oldCheck }
}
```

  Add `"github.com/r2cuerdame/codesamplex/internal/autoupdate"` to the
  test file's imports.

- [ ] **Step 2: Run the reproduction test from Task 1**

  `go test ./internal/daemon/ -run TestDaemonOnlyInstallRunsTheAutomaticUpdateLoop -v`

  Expected: **FAIL** ("never reached the automatic update check") —
  `internal/daemon` compiles now but `startBackground` still does not call
  the loop. This is the recorded red state for the actual fix in Task 4.

## Task 4: Wire the daemon's background loop to the shared updater

**Files:**
- Modify: `internal/daemon/daemon.go`

**Interfaces:**
- Consumes: `autoupdate.Loop`, `autoupdate.Outcome`.
- Produces: `Daemon.Executable string` field (empty means resolve via
  `os.Executable()`), unexported `(*Daemon).runAutomaticUpdates(ctx
  context.Context)`.

- [ ] **Step 1: Add the `Executable` field and import**

  In the `Daemon` struct (after `HTTP *http.Client`), add:

```go
	// Executable overrides the running binary path used by the automatic
	// update loop. Empty (the production default) resolves it via
	// os.Executable() at the time startBackground runs. Tests set this to
	// a fake owned executable so the loop's OwnsExecutable check can pass
	// without touching the real installed csx binary.
	Executable string
```

  Add `"github.com/r2cuerdame/codesamplex/internal/autoupdate"` and
  `csxupdate "github.com/r2cuerdame/codesamplex/internal/update"` to the
  import block.

- [ ] **Step 2: Add the loop runner**

```go
// runAutomaticUpdates is the daemon-only half of contract #457: the
// background sync daemon is the one long-running process a normal
// standalone community install always has, so it is the one that must own
// the bounded signed update check when nobody has started `csx mcp` or
// `csx worker start`. It runs the identical shared loop MCP and the worker
// use (internal/autoupdate), so every existing trust boundary — consent,
// ownership, signature/hash/size verification, update.lock serialization —
// applies unchanged, and a concurrent MCP/worker check on the same home
// simply queues behind the same file lock rather than racing it.
//
// Applying an update replaces only files on disk (the standalone
// executable's replacement, or a new Windows launcher payload directory);
// it never touches this already-running process's loaded code or its open
// listeners. The next process that calls daemon.EnsureRunning with today's
// build version — any `csx mcp`, or an explicit `csx daemon start` — will
// see the version mismatch and replace this daemon for it. Until then,
// `csx daemon status` and `csx update status` say so explicitly instead of
// leaving it silent.
func (d *Daemon) runAutomaticUpdates(ctx context.Context) {
	exe := d.Executable
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return
		}
	}
	for outcome := range autoupdate.Loop(ctx, d.Home, d.Cfg, exe, Version) {
		if outcome.Result.Applied {
			log.Printf("csx daemon: verified csx %s installed; restart the daemon (csx daemon stop && csx daemon start) or run any other csx command to activate it", outcome.Result.LatestVersion)
			continue
		}
		if outcome.Result.ManualInstallRequired {
			log.Printf("csx daemon: signed update %s needs the Windows launcher migration or a newer launcher protocol; rerun the official installer", outcome.Result.LatestVersion)
			continue
		}
		if outcome.Err != nil && ctx.Err() == nil {
			log.Printf("csx daemon: automatic update check failed: %v", outcome.Err)
		}
	}
}
```

- [ ] **Step 3: Call it from `startBackground`**

  In `startBackground`, before the closing `}` of the function, add:

```go
	// Unconditional: the shared loop's own AutoEnabled/OwnsExecutable checks
	// are the single source of truth for whether this install may make an
	// automatic update request, exactly as they already are for MCP and the
	// worker. Gating a second time here would duplicate that policy instead
	// of reusing it.
	go d.runAutomaticUpdates(ctx)
```

- [ ] **Step 4: Run the reproduction test — now green**

  `go test ./internal/daemon/ -run TestDaemonOnlyInstallRunsTheAutomaticUpdateLoop -v`

  Expected: **PASS**.

- [ ] **Step 5: Run the existing local-only/uninitialized silence test**

  `go test ./internal/daemon/ -run TestLocalOnlyAndUninitializedSyncNeverContactNetwork -v`

  Expected: **PASS** (still zero requests — the new goroutine's own
  `AutoEnabled` check keeps it silent in these modes; `d.Executable` is
  empty in that test so it resolves to the `go test` binary, which is a
  no-op the moment `OwnsExecutable` fails).

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/autoupdate_test.go
git commit -m "fix(daemon): run the automatic update loop even when only the daemon is running

Fixes #457"
```

## Task 5: Policy-boundary regression tests (off / local-only / unowned)

**Files:**
- Modify: `internal/daemon/autoupdate_test.go`

**Interfaces:**
- Consumes: `stubAutomaticUpdateForTest` (Task 3), `newTestHome` (existing
  helper in `internal/daemon/daemon_test.go`).

- [ ] **Step 1: Write the three failing-would-be-silent-violation tests**

```go
func TestDaemonOnlyAutoUpdateOffMakesNoCheck(t *testing.T) {
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeCommunity
		c.AutoUpdate = "off"
	})
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
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.Executable = exe

	var checks atomic.Int32
	restoreInterval, restoreCheck := stubAutomaticUpdateForTest(t, 10*time.Millisecond, func() { checks.Add(1) })
	defer restoreInterval()
	defer restoreCheck()

	ctx, cancel := context.WithCancel(context.Background())
	d.startBackground(ctx)
	time.Sleep(150 * time.Millisecond)
	cancel()

	if got := checks.Load(); got != 0 {
		t.Fatalf("autoUpdate=off made %d automatic update checks", got)
	}
}

func TestDaemonOnlyLocalOnlyMakesNoCheck(t *testing.T) {
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeLocalOnly
	})
	exe := filepath.Join(t.TempDir(), "csx")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Ownership can exist even in local-only mode (a user can flip modes
	// later); AutoEnabled must refuse on mode alone.
	if err := csxupdate.AdoptStandalone(home, exe); err != nil {
		t.Fatal(err)
	}
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.Executable = exe

	var checks atomic.Int32
	restoreInterval, restoreCheck := stubAutomaticUpdateForTest(t, 10*time.Millisecond, func() { checks.Add(1) })
	defer restoreInterval()
	defer restoreCheck()

	ctx, cancel := context.WithCancel(context.Background())
	d.startBackground(ctx)
	time.Sleep(150 * time.Millisecond)
	cancel()

	if got := checks.Load(); got != 0 {
		t.Fatalf("local-only made %d automatic update checks", got)
	}
}

func TestDaemonOnlyUnownedExecutableNeverSelfModifies(t *testing.T) {
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeCommunity
		c.AutoUpdate = "auto"
	})
	exe := filepath.Join(t.TempDir(), "csx")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Deliberately never AdoptStandalone: this executable is not the one
	// the install marker owns (e.g. a `go build` dev binary, or a package-
	// manager-owned MCPB payload).
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.Executable = exe

	var checks atomic.Int32
	restoreInterval, restoreCheck := stubAutomaticUpdateForTest(t, 10*time.Millisecond, func() { checks.Add(1) })
	defer restoreInterval()
	defer restoreCheck()

	ctx, cancel := context.WithCancel(context.Background())
	d.startBackground(ctx)
	time.Sleep(150 * time.Millisecond)
	cancel()

	if got := checks.Load(); got != 0 {
		t.Fatalf("unowned executable made %d automatic update checks", got)
	}
	if got, err := os.ReadFile(exe); err != nil || string(got) != "old" {
		t.Fatalf("unowned executable was modified: %q, err=%v", got, err)
	}
}
```

- [ ] **Step 2: Run them**

  `go test ./internal/daemon/ -run TestDaemonOnly -v` — expect all PASS
  (they pass immediately given Task 4's implementation, since `AutoEnabled`
  and `OwnsExecutable` are the real, unmodified `internal/update`
  functions — this step is confirming the reuse is correct, not writing new
  production code).

- [ ] **Step 3: Commit**

```bash
git add internal/daemon/autoupdate_test.go
git commit -m "test(daemon): cover autoUpdate=off, local-only, and unowned-executable silence"
```

## Task 6: Concurrent daemon + MCP-shaped loop safety test

**Files:**
- Create: `internal/autoupdate/concurrent_test.go`

**Interfaces:**
- Consumes: `autoupdate.Loop`, real (unstubbed) `RunCheck` (default), a
  fake signed-manifest HTTP transport built the same way
  `internal/update/client_test.go`'s `clientFixture` does (duplicated
  locally — it is unexported there and small enough to copy rather than
  export a test helper across a package boundary for one test).

- [ ] **Step 1: Write the concurrency test**

```go
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

// Two long-running processes (an MCP-shaped loop and a daemon-shaped loop)
// point at the same home and the same signed manifest at once. Neither may
// corrupt update/state.json or apply two different versions; the existing
// update.lock file serialization (proven directly against Client in
// internal/update/client_test.go) must make this safe end to end through
// the shared Loop wrapper too.
func TestConcurrentLoopsOnTheSameHomeDoNotRace(t *testing.T) {
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
		return client.Check(ctx, true)
	}
	t.Cleanup(func() { RunCheck = oldCheck })

	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.AutoUpdate = "auto"

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

	applied := 0
	for _, o := range append(a, b...) {
		if o.Err != nil && !strings.Contains(o.Err.Error(), "replayed") {
			t.Fatalf("unexpected error: %v", o.Err)
		}
		if o.Result.Applied {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("exactly one loop should have applied the update, got %d", applied)
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
```

- [ ] **Step 2: Run it**

  `go test ./internal/autoupdate/... -run TestConcurrentLoopsOnTheSameHomeDoNotRace -v -race`

  Expected: PASS. (If it is flaky on `applied != 1`, that means the second
  loop's `Due()` check raced ahead of the first's `SaveState` in a way the
  existing lock does not cover — stop and treat that as a real finding
  against `internal/update`, not something to paper over here.)

- [ ] **Step 3: Commit**

```bash
git add internal/autoupdate/concurrent_test.go
git commit -m "test(autoupdate): prove concurrent daemon+MCP-shaped loops cannot race update.lock"
```

## Task 7: Surface the pending-restart notice on `csx daemon status`

**Files:**
- Modify: `internal/daemon/api.go`
- Modify: `internal/cli/daemon.go`
- Test: `internal/daemon/api_test.go` (existing file — add one test) or a
  new `internal/daemon/autoupdate_test.go` case if `api_test.go` does not
  exist yet (check first with Glob).

**Interfaces:**
- Produces: `StatusInfo.UpdatePendingRestart string` (json
  `updatePendingRestart,omitempty`).
- Consumes: `csxupdate.LoadState(home) (csxupdate.State, error)`.

- [ ] **Step 1: Add the field and populate it**

  In `internal/daemon/api.go`, add to `StatusInfo`:

```go
	// UpdatePendingRestart is set when a signed update has been applied to
	// disk but this daemon process has not been restarted to run it yet.
	// The install already moved to current stable; this says the *running
	// process* has not caught up, and how to fix that.
	UpdatePendingRestart string `json:"updatePendingRestart,omitempty"`
```

  Add `csxupdate "github.com/r2cuerdame/codesamplex/internal/update"` to
  the import block, and in `handleStatus`, after the existing
  `LastUploadError` block, add:

```go
	if ust, err := csxupdate.LoadState(d.Home); err == nil && ust.PendingRestart != "" {
		st.UpdatePendingRestart = ust.PendingRestart
	}
```

- [ ] **Step 2: Print it from `csx daemon status`**

  In `internal/cli/daemon.go`'s `daemonMain`, `case "status"`, after the
  existing `LastUploadError` print, add:

```go
		if st.UpdatePendingRestart != "" {
			fmt.Printf("  update pending restart: %s (run `csx daemon stop && csx daemon start`, or any other csx command, to activate it)\n", st.UpdatePendingRestart)
		}
```

- [ ] **Step 3: Write the status test**

```go
func TestDaemonStatusSurfacesAPendingRestartFromASharedUpdateState(t *testing.T) {
	home := newTestHome(t, nil)
	d, c := startDaemon(t, home)
	ctx := context.Background()

	if err := csxupdate.SaveState(home, csxupdate.State{Schema: 1, PendingRestart: "v9.9.9"}); err != nil {
		t.Fatal(err)
	}

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.UpdatePendingRestart != "v9.9.9" {
		t.Fatalf("status did not surface the pending restart: %+v", st)
	}
	_ = d
}
```

  Place this in `internal/daemon/autoupdate_test.go`, adding
  `csxupdate "github.com/r2cuerdame/codesamplex/internal/update"` to its
  imports if not already present from Task 3/5.

- [ ] **Step 4: Run it**

  `go test ./internal/daemon/ -run TestDaemonStatusSurfacesAPendingRestart -v`

  Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/api.go internal/cli/daemon.go internal/daemon/autoupdate_test.go
git commit -m "feat(daemon): surface a pending-restart notice on csx daemon status"
```

## Task 8: Documentation

**Files:**
- Modify: `README.md`
- Modify: `llms-install.md`

**Interfaces:** none (prose only).

- [ ] **Step 1: Fix the README's automatic-update claim**

  In `README.md`, the "Agent adapter (MCP)" section currently says:

  > Standalone community installs auto-update over an Ed25519-signed
  > manifest with `csx update rollback` available; `local-only` installs
  > make no update request.

  This was already broadly true in wording but the mechanism was
  incomplete; add a clause naming the daemon as an update owner, e.g.:

  > Standalone community installs auto-update over an Ed25519-signed
  > manifest — checked by whichever of the background sync daemon, `csx
  > mcp`, or `csx worker start` is running, so a plain CLI+daemon install
  > with neither MCP nor the worker running still updates itself — with
  > `csx update rollback` available; `local-only` installs make no update
  > request.

- [ ] **Step 2: Fix the "Signed automatic updates" section of `llms-install.md`**

  The paragraph beginning "The first-party installers register the
  installed absolute path..." currently implies the six-hour check happens
  simply because an install is "community" without naming which process
  performs it. Add a short paragraph directly after it:

```markdown
Three processes can perform this check, and only one needs to be running:
the background sync daemon `csx init` already starts in community mode,
`csx mcp`, and `csx worker start`. All three share the same
`update/update.lock` and `update/state.json` under `CSX_HOME`, so running
more than one at once serializes safely instead of racing. A daemon-only
install — the common case for a user who only ever runs `csx search` /
`csx run` from a terminal — updates the binary on disk the same way; the
one thing it cannot do is restart its own already-running process, so
`csx daemon status` reports `update pending restart: vX.Y.Z` until the
daemon is restarted (`csx daemon stop && csx daemon start`) or any other
csx command runs and replaces it automatically (`daemon.EnsureRunning`
detects the version mismatch and swaps it in).
```

- [ ] **Step 3: Commit**

```bash
git add README.md llms-install.md
git commit -m "docs: document that the daemon, not only MCP/worker, owns automatic updates"
```

## Task 9: Full verification pass

- [ ] **Step 1: Focused package tests**

```bash
go test ./internal/autoupdate/... -v -race
go test ./internal/daemon/... -run 'TestDaemonOnly|TestDaemonStatus|TestLocalOnly|TestRunReloads' -v -race
go test ./internal/cli/... -v -race
go test ./internal/update/... -v -race
```

- [ ] **Step 2: Broad suite**

```bash
go build ./...
go vet ./...
go test ./...
```

  (PostgreSQL-backed `internal/serverstore` tests skip without
  `CSX_TEST_DSN`; that is expected locally per README/docs/operations.md —
  do not attempt to stand up Postgres for this hotfix unless something in
  `internal/serverstore` was touched, which it is not.)

- [ ] **Step 3: `csx help` / CLI-surface pinning test**

```bash
go test ./internal/cli/... -run TestCLISurface -v
```

  (Confirms the README's `<!-- BEGIN:CSX-CLI-SURFACE -->` table, unaffected
  by this change, still matches the binary — run only as a safety check
  since `daemon`/`update`/`mcp`/`worker` command summaries were not
  touched.)

- [ ] **Step 4: Record exact evidence for the PR description**

  Capture the full `go test ./...` tail (pass/fail counts, skipped
  packages and why) and the three focused command outputs from Step 1 to
  paste verbatim into the PR body.

## Task 10: Open the pull request

- [ ] **Step 1: Push the branch**

```bash
git push -u origin r2cuerdame/csx-457-autoupdate
```

- [ ] **Step 2: Open the PR against `main`**

  Title: `fix(daemon): run the promised automatic updater when only the background daemon is running`

  Body covers: problem (quote issue #457's evidence section), fix (daemon
  now runs the same shared `internal/autoupdate` loop MCP/worker already
  used, extracted with no change to `internal/update`'s trust boundary),
  what was deliberately left alone (production deploy/release scope, the
  v0.1.197 runtime-isolation milestone, real-binary v0.1.87/v0.1.97
  version-hop coverage — already regression-tested against `internal/update`
  directly and unaffected since `internal/update.Client` is unmodified),
  and the exact test evidence from Task 9.

  Do not deploy production as part of this PR — the issue and
  `docs/operations.md`'s release-then-deploy ordering rule both call for
  reconciling with the v0.1.197 milestone first.
