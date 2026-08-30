package update

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

// P1. The launcher declares payload-start-failed repairable and routes it here,
// but a payload whose BYTES are fine returns ErrRehydrateNotNeeded — so the one
// failure that says "these bytes hash correctly and still will not run" was the
// one the repair refused to act on. An install whose payload is blocked from
// executing (an ACL, an execution policy, a partially-quarantined file that
// still hashes) stayed dead with a repair path that reported nothing to do.
func TestAStartFailureRepairsEvenWhenTheBytesStillHash(t *testing.T) {
	root, current, _ := healthyInstallWithFallback(t)
	srv := newReleaseServer(t, releaseBodies())

	opts := srv.options()
	opts.StartFailed = true
	report, err := RehydrateInstall(context.Background(), root, opts)
	if err != nil {
		t.Fatalf("a start failure was not repaired: %v", err)
	}
	if !contains(report.Restored, current.Version) {
		t.Errorf("restored %v, want the current payload replaced from the release", report.Restored)
	}
	if err := launcher.VerifyPayload(root, current); err != nil {
		t.Errorf("the replaced payload does not verify: %v", err)
	}
}

// Without that signal nothing changes: a healthy install is still left alone.
func TestAHealthyInstallIsStillLeftAlone(t *testing.T) {
	root, _, _ := healthyInstallWithFallback(t)
	srv := newReleaseServer(t, releaseBodies())
	if _, err := RehydrateInstall(context.Background(), root, srv.options()); err == nil ||
		!strings.Contains(err.Error(), "already verified") {
		t.Fatalf("err = %v, want ErrRehydrateNotNeeded", err)
	}
}

// P2. ExhaustedVersion is strong evidence: csx update status prints "had no
// verified fallback left on this machine" from it, and release quality is
// judged on it. It was set from the CURRENT payload alone, before the recorded
// fallback was looked at — so an install with a perfectly good previous, which
// launcher.Resolve would recover onto, reported its recovery set as spent.
func TestAUsableFallbackIsNotReportedAsExhausted(t *testing.T) {
	root, current, previous := healthyInstallWithFallback(t)
	// Only the current is lost. The recorded previous is untouched and
	// verifies, which is exactly the state R2C-181's recovery is FOR.
	p, err := launcher.PayloadPath(root, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := launcher.VerifyPayload(root, previous); err != nil {
		t.Fatalf("fixture's fallback does not verify: %v", err)
	}

	srv := newReleaseServer(t, releaseBodies())
	opts := srv.options()
	opts.Force = true
	report, err := RehydrateInstall(context.Background(), root, opts)
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if report.ExhaustedVersion != "" {
		t.Errorf("ExhaustedVersion = %q while %s was verified on disk; "+
			"status would tell an operator the recovery set was spent when it was not",
			report.ExhaustedVersion, previous.Version)
	}
}

// And the evidence that IS true stays true: both payloads gone is still an
// exhausted install.
func TestBothPayloadsGoneIsStillExhausted(t *testing.T) {
	root, current, _ := exhaustedInstall(t)
	srv := newReleaseServer(t, releaseBodies())
	report, err := RehydrateInstall(context.Background(), root, srv.options())
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if report.ExhaustedVersion != current.Version {
		t.Errorf("ExhaustedVersion = %q, want %s — this install really had nothing left",
			report.ExhaustedVersion, current.Version)
	}
}

// healthyInstallWithFallback is exhaustedInstall's opposite: the pointer is
// correct and both payloads it records are on disk and verify.
func healthyInstallWithFallback(t *testing.T) (string, launcher.Descriptor, launcher.Descriptor) {
	t.Helper()
	root := t.TempDir()
	previous := writePayload(t, root, "v1.0.0", fixtureRehydratedPrevious, 6)
	current := writePayload(t, root, "v1.1.0", fixtureRehydratedCurrent, 7)
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: current, Previous: &previous}); err != nil {
		t.Fatal(err)
	}
	if _, err := launcher.Resolve(root); err != nil {
		t.Fatalf("fixture does not resolve: %v", err)
	}
	return root, current, previous
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
