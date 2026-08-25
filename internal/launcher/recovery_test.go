package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}

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

// descriptorFor is payloadFixture without the file: the caller stages the bytes
// somewhere else and lets CommitPayload put them in their final place.
func descriptorFor(t *testing.T, version, body string, sequence uint64) Descriptor {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	return Descriptor{Version: version, SHA256: hex.EncodeToString(sum[:]), Sequence: sequence}
}
