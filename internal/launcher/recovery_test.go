package launcher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// twoVersionRoot stages a verified previous payload under a verified current
// one, which is the shape every install has after its first update.
func twoVersionRoot(t *testing.T) (string, Descriptor, Descriptor) {
	t.Helper()
	root := t.TempDir()
	previous := payloadFixture(t, root, "v1.0.0", "old", 6)
	current := payloadFixture(t, root, "v1.1.0", "new", 7)
	if err := Write(root, Active{Schema: 1, Current: current, Previous: &previous}); err != nil {
		t.Fatal(err)
	}
	return root, previous, current
}

// A real install lost its payload this way twice: the update committed a
// verified executable and Windows Defender quarantined the file afterwards,
// leaving payloads/<current>/ present but empty. Nothing about the pointer is
// wrong, so the launcher must not treat it as fatal while a verified previous
// payload is still on disk.
func TestResolveFallsBackToVerifiedPreviousWhenCurrentPayloadIsQuarantined(t *testing.T) {
	root, previous, current := twoVersionRoot(t)
	quarantined, _ := PayloadPath(root, current.Version)
	if err := os.Remove(quarantined); err != nil {
		t.Fatal(err)
	}

	res, err := Resolve(root)
	if err != nil {
		t.Fatalf("resolve refused to recover: %v", err)
	}
	if !res.Recovered || res.Descriptor.Version != previous.Version {
		t.Fatalf("resolution=%+v, want a recovery onto %s", res, previous.Version)
	}
	if res.FailedVersion != current.Version || res.FailedReason != ReasonPayloadMissing {
		t.Fatalf("failed current reported as %s/%s", res.FailedVersion, res.FailedReason)
	}
	if !res.Healed {
		t.Fatalf("pointer was not repaired: %v", res.HealError)
	}

	// The repair has to survive the process: csx update itself loads the
	// pointer, so an unhealed install stays unable to fetch a working payload.
	healed, err := Load(root)
	if err != nil {
		t.Fatalf("healed pointer does not load: %v", err)
	}
	if healed.Current.Version != previous.Version {
		t.Fatalf("healed current=%+v", healed.Current)
	}
	if healed.Previous != nil {
		t.Fatalf("healed pointer kept an unrunnable previous: %+v", healed.Previous)
	}
	// Holding the version that just failed to run keeps the automatic updater
	// from reinstalling the same payload on its next check, and keeps the
	// sequence floor that mergeLauncherFloor reads off the pointer.
	if healed.RollbackHold == nil || healed.RollbackHold.Version != current.Version || healed.RollbackHold.Sequence != current.Sequence {
		t.Fatalf("healed pointer lost the failed version: %+v", healed.RollbackHold)
	}
}

func TestResolveRecoveryCannotOverwriteAConcurrentUpdaterCommit(t *testing.T) {
	root, previous, current := twoVersionRoot(t)
	quarantined, _ := PayloadPath(root, current.Version)
	if err := os.Remove(quarantined); err != nil {
		t.Fatal(err)
	}

	// Hold the exact install-scoped lock the updater owns while publishing a
	// newer verified pointer. The hook only signals that recovery reached lock
	// acquisition; the production acquisition and lock file are unchanged.
	lockPath := filepath.Join(root, ".update.lock")
	if err := os.WriteFile(lockPath, []byte("updater-test "+fmt.Sprint(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalAcquire := acquireRecoveryInstallLock
	entered := make(chan struct{})
	acquireRecoveryInstallLock = func(root string, wait time.Duration) (func(), error) {
		close(entered)
		return originalAcquire(root, wait)
	}
	defer func() { acquireRecoveryInstallLock = originalAcquire }()

	type outcome struct {
		resolution Resolution
		err        error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Resolve(root)
		done <- outcome{resolution: res, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery never reached the shared install lock")
	}

	newCurrent := payloadFixture(t, root, "v1.2.0", "newest", 8)
	if err := Write(root, Active{Schema: Schema, Current: newCurrent, Previous: &previous}); err != nil {
		t.Fatal(err)
	}
	// Use the production token-checked bounded release. Windows can retain a
	// read handle for a few milliseconds while recovery inspects liveness; a
	// single raw Remove would test scanner timing rather than lock ownership.
	releaseRecoveryInstallLock(lockPath, "updater-test")

	var got outcome
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery did not resume after the updater released its lock")
	}
	if got.err != nil {
		t.Fatalf("fallback execution was lost: %v", got.err)
	}
	if !got.resolution.Recovered || got.resolution.Descriptor != previous {
		t.Fatalf("resolution=%+v", got.resolution)
	}
	if got.resolution.Healed || got.resolution.HealError == nil {
		t.Fatalf("stale recovery unexpectedly rewrote the pointer: %+v", got.resolution)
	}
	fresh, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Current != newCurrent {
		t.Fatalf("recovery overwrote updater current: got %+v want %+v", fresh.Current, newCurrent)
	}
}

// The launcher has to participate in the updater's stale-owner protocol. If a
// crashed updater leaves this lock behind while current is quarantined,
// refusing to reclaim it leaves the pointer invalid; then the fallback
// payload's update command fails ownership checks before updater-side stale
// lock cleanup can run.
func TestResolveReclaimsADeadUpdaterLockBeforeHealing(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("safe stale-lock takeover uses Windows handle identity pinning")
	}
	root, previous, current := twoVersionRoot(t)
	currentPath, _ := PayloadPath(root, current.Version)
	if err := os.Remove(currentPath); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, ".update.lock")
	if err := os.WriteFile(lockPath, []byte("deadbeef 999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res, err := Resolve(root)
	if err != nil {
		t.Fatalf("dead updater lock prevented recovery: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("dead updater lock took %s to reclaim", elapsed)
	}
	if !res.Healed || res.Descriptor != previous {
		t.Fatalf("resolution=%+v", res)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reclaimed lock remained after recovery: %v", err)
	}
}

func TestRecoveryPinsStaleLockIdentityUntilDisposition(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows pathname replacement regression")
	}
	root := t.TempDir()
	lockPath := filepath.Join(root, ".update.lock")
	if err := os.WriteFile(lockPath, []byte("deadbeef 999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	originalHook := recoveryLockBeforeDisposition
	inspected := make(chan struct{})
	resume := make(chan struct{})
	recoveryLockBeforeDisposition = func() {
		close(inspected)
		<-resume
	}
	defer func() { recoveryLockBeforeDisposition = originalHook }()

	type outcome struct {
		unlock func()
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		unlock, err := acquireRecoveryInstallLock(root, 2*time.Second)
		done <- outcome{unlock: unlock, err: err}
	}()
	select {
	case <-inspected:
	case <-time.After(2 * time.Second):
		t.Fatal("takeover never inspected the stale lock")
	}

	// This is the old P1 interleaving: a successor tries to remove the stale
	// path after recovery inspected it. The pinned handle must make replacement
	// impossible until disposition targets that exact old file.
	if err := os.Remove(lockPath); err == nil {
		close(resume)
		t.Fatal("stale lock pathname was replaceable during conditional deletion")
	}
	close(resume)

	var got outcome
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("takeover did not complete")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer got.unlock()
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(raw), "deadbeef ") {
		t.Fatalf("takeover left the stale identity in place: %q", raw)
	}
}

func TestMalformedRecoveryLockBecomesStaleOnlyAfterAFullDay(t *testing.T) {
	for _, raw := range [][]byte{[]byte("garbage"), []byte("token not-a-pid\n")} {
		if recoveryInstallLockRecordIsStale(raw, time.Now()) {
			t.Fatalf("fresh malformed lock was reclaimed: %q", raw)
		}
		if !recoveryInstallLockRecordIsStale(raw, time.Now().Add(-48*time.Hour)) {
			t.Fatalf("old malformed lock remained permanent: %q", raw)
		}
	}
}

func TestRecoveryNeverDeletesAReparseTargetAsAStaleLock(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows reparse-point deletion regression")
	}
	root := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	want := []byte("deadbeef 999999\n")
	if err := os.WriteFile(victim, want, 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(victim, when, when); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, ".update.lock")
	if err := os.Symlink(victim, lockPath); err != nil {
		t.Skipf("creating a Windows file symlink requires Developer Mode or privilege: %v", err)
	}

	if unlock, err := acquireRecoveryInstallLock(root, 150*time.Millisecond); err == nil {
		unlock()
		t.Fatal("recovery treated a reparse point as a stale lock")
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("recovery deleted the reparse target: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("recovery changed the reparse target: got %q want %q", got, want)
	}
	if fi, err := os.Lstat(lockPath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("recovery changed the lock reparse point: mode=%v err=%v", fi, err)
	}
}

func TestRecoveryNeverTakesAUpdaterLockFromALiveOwner(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(root, ".update.lock")
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("live-owner %d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(lockPath, when, when); err != nil {
		t.Fatal(err)
	}

	if unlock, err := acquireRecoveryInstallLock(root, 150*time.Millisecond); err == nil {
		unlock()
		t.Fatal("recovery took an old lock from a live updater")
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("recovery removed a live updater's lock: %v", err)
	}
}

func TestLiveOwnerReleaseIsNotStarvedByRecoveryProbes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows delete-share regression")
	}
	root := t.TempDir()
	lockPath := filepath.Join(root, ".update.lock")
	const token = "live-release"
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%s %d\n", token, os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var probes sync.WaitGroup
	for i := 0; i < 8; i++ {
		probes.Add(1)
		go func() {
			defer probes.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _, _, _ = tryTakeOverRecoveryInstallLock(lockPath, "probe")
				}
			}
		}()
	}
	releaseRecoveryInstallLock(lockPath, token)
	close(stop)
	probes.Wait()
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live owner release was starved by recovery probes: %v", err)
	}
}

func TestResolveClassifiesEveryInvalidCurrentShape(t *testing.T) {
	for name, tc := range map[string]struct {
		break_ func(t *testing.T, root, payload string)
		reason string
	}{
		"directory missing": {
			break_: func(t *testing.T, root, payload string) {
				if err := os.RemoveAll(filepath.Dir(payload)); err != nil {
					t.Fatal(err)
				}
			},
			reason: ReasonPayloadMissing,
		},
		"directory present but empty": {
			break_: func(t *testing.T, root, payload string) {
				if err := os.Remove(payload); err != nil {
					t.Fatal(err)
				}
			},
			reason: ReasonPayloadMissing,
		},
		"partially written payload": {
			break_: func(t *testing.T, root, payload string) {
				if err := os.WriteFile(payload, []byte("ne"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			reason: ReasonPayloadCorrupt,
		},
		"payload replaced by a directory": {
			break_: func(t *testing.T, root, payload string) {
				if err := os.Remove(payload); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(payload, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			reason: ReasonPayloadNotRegular,
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, previous, current := twoVersionRoot(t)
			payload, _ := PayloadPath(root, current.Version)
			tc.break_(t, root, payload)

			res, err := Resolve(root)
			if err != nil {
				t.Fatalf("resolve refused to recover: %v", err)
			}
			if res.FailedReason != tc.reason {
				t.Fatalf("reason=%q want %q", res.FailedReason, tc.reason)
			}
			if !res.Recovered || res.Descriptor.Version != previous.Version {
				t.Fatalf("resolution=%+v", res)
			}
		})
	}
}

// With no verified payload left there is nothing to run, and the one thing the
// launcher must never do is look like it ran something. The failure carries a
// stable reason so a caller can tell "this install is broken" from "the command
// you asked for failed".
func TestResolveFailsWithAStableReasonWhenNoVerifiedPayloadRemains(t *testing.T) {
	root := t.TempDir()
	current := payloadFixture(t, root, "v1.1.0", "new", 7)
	if err := Write(root, Active{Schema: 1, Current: current}); err != nil {
		t.Fatal(err)
	}
	payload, _ := PayloadPath(root, current.Version)
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}

	res, err := Resolve(root)
	if err == nil {
		t.Fatalf("resolve returned %+v for an install with no runnable payload", res)
	}
	if got := Reason(err); got != ReasonPayloadMissing {
		t.Fatalf("reason=%q", got)
	}
	if !strings.Contains(err.Error(), "v1.1.0") {
		t.Fatalf("error does not name the failed version: %v", err)
	}
}

// An unreadable or absent pointer is its own reason: the install is not merely
// missing a payload, there is nothing to fall back from.
func TestResolveReportsAnUnreadablePointerDistinctly(t *testing.T) {
	root := t.TempDir()
	if _, err := Resolve(root); Reason(err) != ReasonPointerUnreadable {
		t.Fatalf("missing pointer reason=%q err=%v", Reason(err), err)
	}
	if err := os.WriteFile(Path(root), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root); Reason(err) != ReasonPointerUnreadable {
		t.Fatalf("corrupt pointer reason=%q err=%v", Reason(err), err)
	}
}

// Recovery reads only descriptors the pointer already recorded with a hash. A
// payload directory sitting on disk from an older release was never verified by
// this pointer, so it is not a fallback candidate.
func TestResolveNeverAdoptsAnUnrecordedPayloadDirectory(t *testing.T) {
	root := t.TempDir()
	payloadFixture(t, root, "v0.9.0", "stranger", 3)
	current := payloadFixture(t, root, "v1.1.0", "new", 7)
	if err := Write(root, Active{Schema: 1, Current: current}); err != nil {
		t.Fatal(err)
	}
	payload, _ := PayloadPath(root, current.Version)
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}
	if res, err := Resolve(root); err == nil {
		t.Fatalf("resolve adopted an unverified payload directory: %+v", res)
	}
}

// A staging artifact is not a version. PayloadPath already refuses anything
// that is not a canonical vX.Y.Z, which is what keeps a half-extracted
// .staging-* directory from ever being addressable as current.
func TestPayloadPathRefusesStagingAndTraversalVersions(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{".staging-v1.0.0", "v1.0.0.staging", "..", "v1.0.0/../v2.0.0", "v1.0.0-rc1", ""} {
		if _, err := PayloadPath(root, version); err == nil {
			t.Fatalf("version %q was accepted as a payload path", version)
		}
	}
}

// Rollback is the documented recovery for a bad update, so it has to work in
// the one state that makes people reach for it: a current payload that will not
// run. Loading -- and therefore verifying -- current first made that impossible.
func TestRollbackRecoversFromAnUnrunnableCurrent(t *testing.T) {
	root, previous, current := twoVersionRoot(t)
	payload, _ := PayloadPath(root, current.Version)
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}

	next, err := Rollback(root)
	if err != nil {
		t.Fatalf("rollback refused a broken current: %v", err)
	}
	if next.Current.Version != previous.Version {
		t.Fatalf("rollback current=%+v", next.Current)
	}
	if next.Previous != nil {
		t.Fatalf("rollback recorded an unrunnable payload as previous: %+v", next.Previous)
	}
	if next.RollbackHold == nil || next.RollbackHold.Version != current.Version {
		t.Fatalf("rollback lost the held version: %+v", next.RollbackHold)
	}
	if _, err := Load(root); err != nil {
		t.Fatalf("pointer left unloadable after rollback: %v", err)
	}
}

// RollbackHold is a rejection marker, not a second LKG slot. Rollback keeps
// the rejected descriptor in Previous for ownership/history today, but a
// later failure of the rolled-back current must not silently reactivate the
// release the operator explicitly rejected.
func TestResolveNeverReactivatesARollbackHeldPayload(t *testing.T) {
	root, previous, rejected := twoVersionRoot(t)
	next, err := Rollback(root)
	if err != nil {
		t.Fatal(err)
	}
	if next.Current != previous || next.Previous == nil || next.RollbackHold == nil ||
		!samePayload(*next.Previous, rejected) || !samePayload(*next.RollbackHold, rejected) {
		t.Fatalf("rollback did not preserve the expected history: %+v", next)
	}
	rolledBackPath, _ := PayloadPath(root, previous.Version)
	if err := os.Remove(rolledBackPath); err != nil {
		t.Fatal(err)
	}

	if res, err := Resolve(root); err == nil {
		t.Fatalf("resolve reactivated the held release: %+v", res)
	} else if Reason(err) != ReasonPayloadMissing {
		t.Fatalf("reason=%q err=%v", Reason(err), err)
	}
	unchanged, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Current != previous || unchanged.RollbackHold == nil || !samePayload(*unchanged.RollbackHold, rejected) {
		t.Fatalf("failed-closed resolve rewrote the pointer: %+v", unchanged)
	}
}

// A rejected release is retained as Hold metadata, not as a required LKG.
// Defender removing that already-rejected payload must not make the healthy
// rolled-back current fail updater preflight or prevent a newer verified
// payload from preserving the real current as Previous.
func TestMissingRollbackHeldPayloadDoesNotBlockTheNextUpdate(t *testing.T) {
	root, previous, rejected := twoVersionRoot(t)
	_, err := Rollback(root)
	if err != nil {
		t.Fatal(err)
	}
	rejectedPath, _ := PayloadPath(root, rejected.Version)
	if err := os.Remove(rejectedPath); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatalf("healthy rolled-back current no longer loads: %v", err)
	}
	if err := Validate(root, loaded); err != nil {
		t.Fatalf("rejected missing Hold blocked updater preflight: %v", err)
	}
	staged := filepath.Join(root, "staged-after-hold.exe")
	if err := os.WriteFile(staged, []byte("new after rejection"), 0o700); err != nil {
		t.Fatal(err)
	}
	next, err := CommitPayload(root, staged, descriptorFor(t, "v1.2.0", "new after rejection", rejected.Sequence+1))
	if err != nil {
		t.Fatalf("new verified update remained blocked: %v", err)
	}
	if next.Previous == nil || *next.Previous != previous {
		t.Fatalf("new update did not preserve the healthy current as LKG: %+v", next)
	}
}

func TestSequencePromotionCannotMakeRollbackHeldPayloadEligibleAgain(t *testing.T) {
	root, _, rejected := twoVersionRoot(t)
	rolledBack, err := Rollback(root)
	if err != nil {
		t.Fatal(err)
	}
	currentPath, _ := PayloadPath(root, rolledBack.Current.Version)
	staged := filepath.Join(root, "same-current.exe")
	raw, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	promoted := rolledBack.Current
	promoted.Sequence = rejected.Sequence + 20
	afterPromotion, err := CommitPayload(root, staged, promoted)
	if err != nil {
		t.Fatal(err)
	}
	if afterPromotion.Previous == nil || afterPromotion.RollbackHold == nil ||
		!samePayload(*afterPromotion.Previous, rejected) || !samePayload(*afterPromotion.RollbackHold, rejected) {
		t.Fatalf("sequence promotion lost rejection metadata: %+v", afterPromotion)
	}
	if next, err := Rollback(root); err == nil {
		t.Fatalf("sequence promotion re-enabled rejected payload: %+v", next)
	} else if !strings.Contains(err.Error(), "rollback-held") {
		t.Fatalf("rollback failed for the wrong reason: %v", err)
	}
	unchanged, err := Load(root)
	if err != nil || unchanged.Current != promoted {
		t.Fatalf("rejected rollback changed current: %+v err=%v", unchanged, err)
	}
}

// A commit that fails partway must leave nothing addressable behind. An empty
// payloads/<version>/ is exactly the shape an invalid current takes on disk, so
// the directory this call created goes away with it.
func TestCommitPayloadLeavesNoEmptyVersionDirectoryBehind(t *testing.T) {
	root := t.TempDir()
	current := payloadFixture(t, root, "v1.0.0", "old", 6)
	if err := Write(root, Active{Schema: 1, Current: current}); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, "staged.exe")
	if err := os.WriteFile(staged, []byte("second"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The descriptor claims a hash the staged file does not have, so the
	// commit fails after the version directory has already been created.
	if _, err := CommitPayload(root, staged, Descriptor{Version: "v1.1.0", SHA256: strings.Repeat("ab", 32), Sequence: 7}); err == nil {
		t.Fatal("commit accepted a payload that does not match its descriptor")
	}
	orphan, _ := PayloadPath(root, "v1.1.0")
	if _, err := os.Stat(filepath.Dir(orphan)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed commit left payloads/v1.1.0 behind: %v", err)
	}
	a, err := Load(root)
	if err != nil || a.Current.Version != "v1.0.0" {
		t.Fatalf("failed commit moved current: %+v err=%v", a, err)
	}
}

// Cleanup after a failed commit must never reach a directory it did not create
// -- the current and last-known-good payloads live under the same parent.
func TestCommitPayloadCleanupNeverRemovesAnExistingVersionDirectory(t *testing.T) {
	root := t.TempDir()
	current := payloadFixture(t, root, "v1.0.0", "old", 6)
	if err := Write(root, Active{Schema: 1, Current: current}); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, "staged.exe")
	if err := os.WriteFile(staged, []byte("mismatch"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitPayload(root, staged, Descriptor{Version: "v1.0.0", SHA256: strings.Repeat("cd", 32), Sequence: 9}); err == nil {
		t.Fatal("commit accepted a mismatched payload for an existing version")
	}
	if _, err := Load(root); err != nil {
		t.Fatalf("cleanup damaged the live install: %v", err)
	}
}

// The happy path stays intact: a verified payload becomes current only after it
// hashes correctly in its final location, and the payload it replaces is kept
// as the last-known-good the recovery above depends on.
func TestCommitPayloadPublishesOnlyAfterVerificationAndKeepsLastKnownGood(t *testing.T) {
	root := t.TempDir()
	current := payloadFixture(t, root, "v1.0.0", "old", 6)
	if err := Write(root, Active{Schema: 1, Current: current}); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, "staged.exe")
	if err := os.WriteFile(staged, []byte("second"), 0o700); err != nil {
		t.Fatal(err)
	}
	next, err := CommitPayload(root, staged, descriptorFor(t, "v1.1.0", "second", 7))
	if err != nil {
		t.Fatal(err)
	}
	if next.Current.Version != "v1.1.0" || next.Previous == nil || next.Previous.Version != "v1.0.0" {
		t.Fatalf("commit=%+v", next)
	}
	res, err := Resolve(root)
	if err != nil || res.Recovered || res.Descriptor.Version != "v1.1.0" {
		t.Fatalf("resolve=%+v err=%v", res, err)
	}
}

// Defender can remove current after updater preflight but before commit. The
// structurally valid pointer still identifies an older verified Previous; the
// new commit must carry that LKG forward instead of dropping all recovery.
func TestCommitPayloadKeepsVerifiedPreviousWhenCurrentDisappearsMidUpdate(t *testing.T) {
	root, previous, current := twoVersionRoot(t)
	currentPath, _ := PayloadPath(root, current.Version)
	if err := os.Remove(currentPath); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, "staged.exe")
	if err := os.WriteFile(staged, []byte("newest"), 0o700); err != nil {
		t.Fatal(err)
	}

	next, err := CommitPayload(root, staged, descriptorFor(t, "v1.2.0", "newest", 8))
	if err != nil {
		t.Fatal(err)
	}
	if next.Previous == nil || *next.Previous != previous {
		t.Fatalf("commit lost the verified LKG: %+v", next)
	}
	newPath, _ := PayloadPath(root, next.Current.Version)
	if err := os.Remove(newPath); err != nil {
		t.Fatal(err)
	}
	res, err := Resolve(root)
	if err != nil || res.Descriptor != previous || !res.Recovered {
		t.Fatalf("second quarantine could not recover: res=%+v err=%v", res, err)
	}
}

// A runnable RollbackHold is still a rejected payload. If current disappears
// during a later update and Previous aliases Hold, commit must not publish that
// rejected artifact as the new release's LKG.
func TestCommitPayloadNeverPromotesRollbackHoldToPrevious(t *testing.T) {
	root, _, rejected := twoVersionRoot(t)
	rolledBack, err := Rollback(root)
	if err != nil {
		t.Fatal(err)
	}
	currentPath, _ := PayloadPath(root, rolledBack.Current.Version)
	if err := os.Remove(currentPath); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, "staged.exe")
	if err := os.WriteFile(staged, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}

	next, err := CommitPayload(root, staged, descriptorFor(t, "v1.2.0", "replacement", rejected.Sequence+1))
	if err != nil {
		t.Fatal(err)
	}
	if next.Previous != nil {
		t.Fatalf("commit promoted rejected payload as LKG: %+v", next.Previous)
	}
	if _, err := os.Stat(mustPayloadPathForTest(t, root, rejected.Version)); err != nil {
		t.Fatalf("test no longer has a runnable held payload: %v", err)
	}
}

func mustPayloadPathForTest(t *testing.T, root, version string) string {
	t.Helper()
	path, err := PayloadPath(root, version)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// descriptorFor is payloadFixture without the file: the caller stages the bytes
// somewhere else and lets CommitPayload put them in their final place.
func descriptorFor(t *testing.T, version, body string, sequence uint64) Descriptor {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	return Descriptor{Version: version, SHA256: hex.EncodeToString(sum[:]), Sequence: sequence}
}
