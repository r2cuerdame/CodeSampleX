package cli

// What `csx sync` says while it waits, and what it does when the daemon
// cannot be reached.
//
// Measured 2026-09-03 on the reporting workstation: fifteen minutes with no
// output, then thirteen identical "context deadline exceeded" lines. The
// user asked for an elapsed line or a spinner (#177). Three pure pieces
// make that testable without a terminal:
//
//   - progressLine renders one stderr line from the daemon's reported
//     progress and the elapsed time;
//   - collapseErrors folds repeats into "N× message";
//   - fallBackInProcess decides when the CLI may run the sync itself. It
//     used to do so whenever the daemon call failed, including when the
//     call merely timed out on a daemon that was still working -- which
//     ran the same sync twice on the same database. Now only an
//     unreachable daemon justifies it.

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/daemon"
)

func TestProgressLineNamesTheStageCountAndElapsed(t *testing.T) {
	line := progressLine(&daemon.SyncProgress{Stage: "warming", Done: 312, Total: 1558}, 72*time.Second)
	for _, want := range []string{"warming", "312/1558", "01:12"} {
		if !containsStr(line, want) {
			t.Fatalf("progress line %q lacks %q", line, want)
		}
	}
	if containsStr(line, "\n") {
		t.Fatalf("progress line must be one rewritable line, got %q", line)
	}
}

func TestProgressLineWithoutADaemonReportStillShowsElapsed(t *testing.T) {
	line := progressLine(nil, 5*time.Second)
	if !containsStr(line, "00:05") {
		t.Fatalf("elapsed missing from %q", line)
	}
}

func TestCollapseErrorsFoldsRepeats(t *testing.T) {
	in := []string{
		"context deadline exceeded",
		"context deadline exceeded",
		"shard npm/x/0: 503",
		"context deadline exceeded",
	}
	got := collapseErrors(in)
	if len(got) != 2 {
		t.Fatalf("collapsed to %d lines, want 2: %v", len(got), got)
	}
	if !containsStr(got[0], "3×") || !containsStr(got[0], "context deadline exceeded") {
		t.Fatalf("first line should count the three repeats: %q", got[0])
	}
	if got[1] != "shard npm/x/0: 503" {
		t.Fatalf("a single error is printed as is, got %q", got[1])
	}
}

func TestFallBackInProcessOnlyWhenTheDaemonIsUnreachable(t *testing.T) {
	unreachable := &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	cases := []struct {
		name     string
		probeErr error
		want     bool
	}{
		{"daemon answered the probe", nil, false},
		{"connection refused", unreachable, true},
		{"probe timed out on a live daemon", context.DeadlineExceeded, false},
		{"some other daemon error", errors.New("daemon: busy"), false},
	}
	for _, tc := range cases {
		if got := fallBackInProcess(tc.probeErr); got != tc.want {
			t.Errorf("%s: fallBackInProcess = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
