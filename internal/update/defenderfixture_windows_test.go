//go:build windows

package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/defender"
)

// The bodies the Windows launcher fixtures write where a payload, a staged
// download or the stable launcher would be.
//
// These are constants rather than literals for one measured reason. Until
// 2026-08-26 the installed-payload fixture wrote the eleven ASCII bytes
// `old-payload`, and Microsoft Defender classifies exactly those eleven bytes
// as `Trojan:Win32/Bearfoos.A!ml` — no PE header, no code, just lowercase
// text. `old-payloa` is clean and `old-payload2` is clean, so nothing about
// the shape of the string predicted it.
//
// On any Windows machine with real-time protection on, that made
// `go test ./internal/update` fail six tests at once with
//
//	open ...\payloads\v1.0.0\csx-payload.exe: Operation did not complete
//	successfully because the file contains a virus or potentially unwanted
//	software.
//
// which reads as an updater bug and is not one. Worse, every such run filed
// its own `csx-payload.exe` quarantine entry into the machine's Defender
// history — the same history this project reads as evidence about released
// payloads. The test suite was contaminating the measurement it feeds.
//
// So the bodies live here next to the guard below, which asks Defender about
// them instead of assuming. Any new fixture body written to an executable path
// belongs in this list.
const (
	fixtureInstalledPayload = "csx test fixture payload: installed version"
	fixtureStagedPayload    = "csx test fixture payload: replacement version"
	fixtureStableLauncher   = "csx test fixture launcher"
)

func windowsFixtureBodies() map[string]string {
	return map[string]string{
		"installed payload":  fixtureInstalledPayload,
		"staged payload":     fixtureStagedPayload,
		"stable launcher":    fixtureStableLauncher,
		"refetched current":  fixtureRehydratedCurrent,
		"refetched previous": fixtureRehydratedPrevious,
		"refetched stranger": fixtureRehydratedStranger,
	}
}

// A fixture body Defender objects to does not fail once and loudly. It fails
// as an unrelated-looking I/O error in whichever test happens to open the file
// first, on developer machines only, and it keeps filing quarantine records
// that look exactly like a released payload being quarantined.
//
// This test converts that into one failure that says what happened and what to
// do about it. It skips where there is no Defender to ask — a verdict nobody
// obtained is not a clean verdict — and it never remediates, so running it
// cannot delete the fixtures it is checking.
func TestWindowsFixtureBodiesAreNotDefenderFalsePositives(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 0, len(windowsFixtureBodies()))
	byPath := map[string]string{}
	for role, body := range windowsFixtureBodies() {
		p := filepath.Join(dir, filepath.Base(role)+".bin")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
		byPath[p] = role
	}

	verdicts, err := defender.Scan(context.Background(), paths...)
	if errors.Is(err, defender.ErrUnavailable) {
		t.Skip("Microsoft Defender command-line scanner is not installed; this guard has nothing to ask")
	}
	if err != nil {
		t.Fatalf("Defender scan of the Windows fixture bodies failed: %v", err)
	}
	for _, v := range verdicts {
		if !v.Flagged {
			continue
		}
		t.Errorf("Defender flags the %s fixture body as %v (definitions %s).\n"+
			"Real-time protection will quarantine it mid-test and the failures will name an updater bug that does not exist.\n"+
			"Change the constant in this file, then rerun; the bytes are arbitrary and only have to be distinct.",
			byPath[v.Path], v.Threats, v.Definitions)
	}
}
