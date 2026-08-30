package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/daemon"
)

// A stage this install reached before anything recorded it is not a stage it
// never reached.
//
// The activation ledger marks those "unmeasured" on purpose — a value rather
// than an absent key, because absence is what a fresh install looks like and
// the two must not read the same. The panel then read them the same anyway:
// an install running for days, with config.json, identity.json and a csx.db
// full of hits, printed
//
//	Initialized  —  never  → run csx init
//
// which tells an operator their install is broken and hands them a command
// that would do nothing. Reported from a farm node whose homes had been up
// for days.
func TestAnUnmeasuredStageIsNotReportedAsNeverReached(t *testing.T) {
	var buf bytes.Buffer
	printReadiness(&buf, daemon.Readiness{
		Unmeasured:     []string{"initAt", "firstRunAt"},
		MCPLastReadyAt: "2026-08-30T14:23:27Z",
	})
	out := buf.String()

	if strings.Contains(out, "run csx init") {
		t.Errorf("an install that is already initialized was told to initialize:\n%s", out)
	}
	for _, label := range []string{"Initialized", "First run"} {
		line := lineWith(out, label)
		if strings.Contains(line, "never") {
			t.Errorf("%q reports a stage that was reached as never reached: %q", label, line)
		}
	}
	// A stage genuinely not reached still says so, with the command that fixes
	// it. The distinction is the whole point.
	if line := lineWith(out, "Shard cache warmed"); !strings.Contains(line, "never") || !strings.Contains(line, "csx sync") {
		t.Errorf("a stage that really was never reached lost its nudge: %q", line)
	}
}

func lineWith(out, label string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, label) {
			return l
		}
	}
	return ""
}
