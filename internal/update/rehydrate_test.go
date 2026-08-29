package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

// The repair fixtures write to executable payload paths, so their bodies are
// declared here and checked against Defender by
// TestWindowsFixtureBodiesAreNotDefenderFalsePositives. See
// defenderfixture_windows_test.go for why arbitrary fixture text is not
// arbitrary on Windows.
const (
	fixtureRehydratedCurrent  = "csx test fixture payload: refetched current"
	fixtureRehydratedPrevious = "csx test fixture payload: refetched previous"
	fixtureRehydratedStranger = "csx test fixture payload: not what the pointer recorded"
)

func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// releaseServer serves the official release layout for the running target and
// counts how many payload requests it answered.
type releaseServer struct {
	*httptest.Server
	requests atomic.Int64
}

func newReleaseServer(t *testing.T, bodies map[string]string) *releaseServer {
	t.Helper()
	rs := &releaseServer{}
	name := "csx-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.requests.Add(1)
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 2 || parts[1] != name {
			http.NotFound(w, r)
			return
		}
		body, ok := bodies[parts[0]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(body, "TRUNCATE:") {
			full := strings.TrimPrefix(body, "TRUNCATE:")
			w.Header().Set("Content-Length", strconv.Itoa(len(full)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(full[:len(full)/2]))
			// Abort mid-body: this is an interrupted download, not a short one
			// the client could mistake for a complete transfer.
			panic(http.ErrAbortHandler)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *releaseServer) options() RehydrateOptions {
	return RehydrateOptions{
		HTTP:     rs.Client(),
		BaseURL:  rs.URL,
		SelfTest: func(context.Context, string, string) error { return nil },
	}
}

// exhaustedInstall is the shape this issue was filed for and the shape the real
// Windows workstation was in on 2026-08-29: the pointer is correct and every
// payload it records is gone from the machine, so launcher.Resolve has nothing
// left to fall back to.
func exhaustedInstall(t *testing.T) (string, launcher.Descriptor, launcher.Descriptor) {
	t.Helper()
	root := t.TempDir()
	previous := writePayload(t, root, "v1.0.0", fixtureRehydratedPrevious, 6)
	current := writePayload(t, root, "v1.1.0", fixtureRehydratedCurrent, 7)
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: current, Previous: &previous}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []launcher.Descriptor{current, previous} {
		p, err := launcher.PayloadPath(root, d.Version)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := launcher.Resolve(root); err == nil {
		t.Fatal("fixture still resolves a payload; it does not reproduce the exhausted install")
	} else if launcher.Reason(err) != launcher.ReasonPayloadMissing {
		t.Fatalf("fixture reason=%q, want %q", launcher.Reason(err), launcher.ReasonPayloadMissing)
	}
	return root, current, previous
}

func writePayload(t *testing.T, root, version, body string, sequence uint64) launcher.Descriptor {
	t.Helper()
	p, err := launcher.PayloadPath(root, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return launcher.Descriptor{Version: version, SHA256: digestOf(body), Sequence: sequence}
}

func readPointerBytes(t *testing.T, root string) []byte {
	t.Helper()
	raw, err := os.ReadFile(launcher.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func releaseBodies() map[string]string {
	return map[string]string{
		"v1.0.0": fixtureRehydratedPrevious,
		"v1.1.0": fixtureRehydratedCurrent,
	}
}

// The whole point: an install with no verified fallback left repairs itself
// from the release it was installed from, and comes back runnable.
func TestRehydrateRestoresAnExhaustedInstallFromTheOfficialRelease(t *testing.T) {
	root, current, previous := exhaustedInstall(t)
	before := readPointerBytes(t, root)
	srv := newReleaseServer(t, releaseBodies())

	report, err := RehydrateInstall(context.Background(), root, srv.options())
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if report.ExhaustedVersion != current.Version {
		t.Fatalf("report names %q as exhausted, want %s", report.ExhaustedVersion, current.Version)
	}
	if strings.Join(report.Restored, ",") != current.Version+","+previous.Version {
		t.Fatalf("restored %v, want both recorded payloads", report.Restored)
	}
	res, err := launcher.Resolve(root)
	if err != nil {
		t.Fatalf("repaired install still does not resolve: %v", err)
	}
	if res.Descriptor.Version != current.Version || res.Recovered {
		t.Fatalf("resolution=%+v, want the recorded current with no recovery", res)
	}

	// The pointer is not this path's to change. It already named the right
	// payload; only the bytes were gone, and every pointer write in this
	// install stays with the verified-only paths that own it.
	if got := readPointerBytes(t, root); string(got) != string(before) {
		t.Fatalf("repair rewrote the active pointer:\n%s\nwant:\n%s", got, before)
	}
}

// The completion condition R2C-236 states directly: after a repair the install
// must hold a verified fallback again, so the next lost payload is recovered on
// this machine instead of needing the network a second time.
func TestRehydrateLeavesAVerifiedFallbackForTheNextLoss(t *testing.T) {
	root, current, previous := exhaustedInstall(t)
	srv := newReleaseServer(t, releaseBodies())

	report, err := RehydrateInstall(context.Background(), root, srv.options())
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if report.FallbackVersion != previous.Version {
		t.Fatalf("repair left fallback %q, want %s", report.FallbackVersion, previous.Version)
	}

	// Quarantine the freshly repaired current again, with the network gone.
	srv.Close()
	p, err := launcher.PayloadPath(root, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	res, err := launcher.Resolve(root)
	if err != nil {
		t.Fatalf("second payload loss was not recovered locally: %v", err)
	}
	if !res.Recovered || res.Descriptor.Version != previous.Version {
		t.Fatalf("resolution=%+v, want a local fallback onto %s", res, previous.Version)
	}
}

// Bytes that are not the exact bytes this install recorded are not a repair,
// whatever they are and wherever they came from.
func TestRehydrateRefusesBytesThatDoNotMatchTheRecordedHash(t *testing.T) {
	root, current, _ := exhaustedInstall(t)
	srv := newReleaseServer(t, map[string]string{
		"v1.0.0": fixtureRehydratedPrevious,
		"v1.1.0": fixtureRehydratedStranger,
	})

	report, err := RehydrateInstall(context.Background(), root, srv.options())
	if err == nil {
		t.Fatalf("repair accepted substituted bytes: %+v", report)
	}
	if !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("error does not say the digest was refused: %v", err)
	}
	p, _ := launcher.PayloadPath(root, current.Version)
	if _, statErr := os.Stat(p); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused bytes reached the payload path: %v", statErr)
	}
	if _, err := launcher.Resolve(root); err == nil {
		t.Fatal("install resolves a payload after a refused repair")
	}
}

// An interrupted transfer cannot become a partially repaired install.
func TestRehydrateFailsClosedOnAnInterruptedTransfer(t *testing.T) {
	root, current, _ := exhaustedInstall(t)
	srv := newReleaseServer(t, map[string]string{"v1.1.0": "TRUNCATE:" + fixtureRehydratedCurrent})

	if report, err := RehydrateInstall(context.Background(), root, srv.options()); err == nil {
		t.Fatalf("repair reported success over an aborted download: %+v", report)
	}
	p, _ := launcher.PayloadPath(root, current.Version)
	if _, statErr := os.Stat(p); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("an interrupted download left a payload behind: %v", statErr)
	}
	// Nothing may be left staged in the install root either: a half-written
	// .csx-rehydrate-*.exe is the shape a later run would have to reason about.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".csx-rehydrate-") {
			t.Fatalf("interrupted repair left %s staged", e.Name())
		}
	}
}

// The failure this repair exists inside: Defender takes the payload again,
// seconds after it was written. Reporting that as a successful repair would
// hand an MCP host an install that still cannot start.
func TestRehydrateFailsClosedWhenTheRepairedPayloadIsQuarantinedAgain(t *testing.T) {
	root, current, _ := exhaustedInstall(t)
	srv := newReleaseServer(t, releaseBodies())
	restore := afterRestoreForTests
	afterRestoreForTests = func(root string, d launcher.Descriptor) {
		if d.Version != current.Version {
			return
		}
		p, _ := launcher.PayloadPath(root, d.Version)
		_ = os.Remove(p)
	}
	t.Cleanup(func() { afterRestoreForTests = restore })

	report, err := RehydrateInstall(context.Background(), root, srv.options())
	if err == nil {
		t.Fatalf("repair reported success over a requarantined payload: %+v", report)
	}
	if !strings.Contains(err.Error(), "did not stay on disk") {
		t.Fatalf("error does not name the requarantine: %v", err)
	}
	rec, ok, recErr := launcher.ReadRehydrateRecord(root)
	if recErr != nil || !ok {
		t.Fatalf("no durable evidence of the failed repair: ok=%v err=%v", ok, recErr)
	}
	if rec.Outcome != launcher.RehydrateOutcomeFailed || rec.ExhaustedVersion != current.Version {
		t.Fatalf("record=%+v", rec)
	}
}

// A corrupt remnant at the immutable version path is the same damage as an
// empty one, and the recorded digest is what decides whether the replacement is
// the right file.
func TestRehydrateReplacesACorruptRemnantAtTheVersionPath(t *testing.T) {
	root, current, _ := exhaustedInstall(t)
	p, _ := launcher.PayloadPath(root, current.Version)
	if err := os.WriteFile(p, []byte(fixtureRehydratedStranger), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := newReleaseServer(t, releaseBodies())

	if _, err := RehydrateInstall(context.Background(), root, srv.options()); err != nil {
		t.Fatalf("repair refused a corrupt remnant it had the authoritative bytes for: %v", err)
	}
	if err := launcher.VerifyPayload(root, current); err != nil {
		t.Fatalf("payload path still does not verify: %v", err)
	}
}

// A pointer that will not parse records no digest, so there is nothing to hold
// a download to. Repairing from it would be the adoption this path refuses.
func TestRehydrateRefusesAnInstallItCannotAnchorToARecordedDigest(t *testing.T) {
	root := t.TempDir()
	srv := newReleaseServer(t, releaseBodies())
	if _, err := RehydrateInstall(context.Background(), root, srv.options()); err == nil {
		t.Fatal("repair ran against an install with no pointer at all")
	}
	if err := os.WriteFile(launcher.Path(root), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RehydrateInstall(context.Background(), root, srv.options()); err == nil {
		t.Fatal("repair ran against an unreadable pointer")
	}
	if srv.requests.Load() != 0 {
		t.Fatalf("repair downloaded %d times without a trustworthy digest", srv.requests.Load())
	}
}

// A healthy install is not repaired, and never downloads.
func TestRehydrateDoesNothingWhenTheCurrentPayloadVerifies(t *testing.T) {
	root := t.TempDir()
	current := writePayload(t, root, "v1.1.0", fixtureRehydratedCurrent, 7)
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: current}); err != nil {
		t.Fatal(err)
	}
	srv := newReleaseServer(t, releaseBodies())
	if _, err := RehydrateInstall(context.Background(), root, srv.options()); !errors.Is(err, ErrRehydrateNotNeeded) {
		t.Fatalf("err=%v, want ErrRehydrateNotNeeded", err)
	}
	if srv.requests.Load() != 0 {
		t.Fatalf("healthy install downloaded %d times", srv.requests.Load())
	}
}

// A machine that is simply offline restarts csx all day. Without a cooldown
// every one of those restarts pays a full network timeout; with one, the
// operator can still force a retry the moment the network is back.
func TestRehydrateBacksOffAfterAFailedAttemptAndAnOperatorCanOverrideIt(t *testing.T) {
	root, _, _ := exhaustedInstall(t)
	srv := newReleaseServer(t, map[string]string{})
	now := time.Now().UTC()
	opts := srv.options()
	opts.Now = func() time.Time { return now }

	if _, err := RehydrateInstall(context.Background(), root, opts); err == nil {
		t.Fatal("repair succeeded against a release that serves nothing")
	}
	first := srv.requests.Load()
	if _, err := RehydrateInstall(context.Background(), root, opts); !errors.Is(err, ErrRehydrateCooldown) {
		t.Fatalf("second attempt err=%v, want ErrRehydrateCooldown", err)
	}
	if srv.requests.Load() != first {
		t.Fatalf("the cooled-down attempt still downloaded: %d then %d", first, srv.requests.Load())
	}

	forced := opts
	forced.Force = true
	if _, err := RehydrateInstall(context.Background(), root, forced); err == nil {
		t.Fatal("forced repair succeeded against a release that serves nothing")
	}
	if srv.requests.Load() == first {
		t.Fatal("an explicit repair was silenced by the cooldown")
	}

	// The cooldown expires; it does not become a permanent refusal to repair.
	opts.Now = func() time.Time { return now.Add(RehydrateCooldown + time.Second) }
	if _, err := RehydrateInstall(context.Background(), root, opts); errors.Is(err, ErrRehydrateCooldown) {
		t.Fatal("the cooldown outlived its own window")
	}
}

// Nothing local decides where a repair downloads from. Without an injected test
// client the URL is rebuilt from the version and target and then checked
// against the same allowlist the signed updater uses.
func TestRepairDownloadsOnlyFromTheOfficialReleasePath(t *testing.T) {
	got, err := releaseURLFor(RehydrateOptions{BaseURL: "https://evil.example/dl"}, "v1.1.0", "windows", "amd64")
	if err != nil {
		t.Fatalf("official URL was refused: %v", err)
	}
	want := DefaultReleaseDownloadBase + "/v1.1.0/csx-windows-amd64.exe"
	if got != want {
		t.Fatalf("repair would download from %q, want %q", got, want)
	}
	if err := validateSignedAssetURL(got, "v1.1.0", "windows", "amd64"); err != nil {
		t.Fatalf("the rebuilt URL is not the signed release target: %v", err)
	}
	for _, version := range []string{"latest", "v1.1", "../v1.1.0", "v1.1.0-rc1"} {
		if url, err := ReleaseAssetURL("", version, "windows", "amd64"); err == nil {
			t.Fatalf("version %q produced a download URL %q", version, url)
		}
	}
}
