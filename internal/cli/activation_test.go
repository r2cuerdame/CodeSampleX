package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func readLedger(t *testing.T, home string) localdb.Activation {
	t.Helper()
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("open local store: %v", err)
	}
	defer db.Close()
	led, err := db.ActivationLedger(context.Background())
	if err != nil {
		t.Fatalf("ActivationLedger: %v", err)
	}
	return led
}

// S1 is "binary first run", and before this it had no signal on either side:
// nothing in $CSX_HOME recorded that a binary was ever executed, so an
// install that downloaded, ran once and gave up was indistinguishable from
// one that was never unpacked.
//
// docs/activation-funnel.md §7 puts the stamp at the top of Main, ahead of
// argv, precisely so the early returns count: `csx` with no arguments, help
// and an unknown command are the runs a stalled install actually makes.
// Stamping from config.EnsureHome instead would mean "first command that
// needed the home", which starts the clock late and flatters every duration
// measured from it.
func TestTheFirstRunIsStampedEvenWhenTheCommandNeverReachesAHandler(t *testing.T) {
	for _, argv := range [][]string{nil, {"help"}, {"--help"}, {"-h"}, {"version"}, {"no-such-command-xyz"}} {
		home := t.TempDir()
		t.Setenv("CSX_HOME", home)
		Main(argv)
		if led := readLedger(t, home); led.FirstRunAt.IsZero() {
			t.Errorf("csx %v recorded no first run", argv)
		}
	}
}

// The ledger's whole value is that it answers "when did this install start",
// so the second run must not overwrite the first. A firstRunAt that advanced
// on every invocation would make time-to-first-value shrink toward zero as
// the install got more use.
func TestALaterRunDoesNotMoveTheFirstRunStamp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	Main([]string{"version"})
	first := readLedger(t, home).FirstRunAt
	if first.IsZero() {
		t.Fatal("first run was not stamped")
	}
	time.Sleep(1100 * time.Millisecond) // the stamp has second resolution
	Main([]string{"version"})
	if again := readLedger(t, home).FirstRunAt; !again.Equal(first) {
		t.Fatalf("firstRunAt moved from %s to %s on the second run", first, again)
	}
}

// S2 is "csx init complete", and the funnel's one durable question — how long
// from install to first useful answer — is measured from it (§5). config.json
// carries the mode but no time, so before this stamp the near end of that
// duration did not exist anywhere.
//
// The stamp goes after cfg.Save because init is complete when the mode is
// persisted: a run that asked the question and then failed to write the
// answer has not initialized anything.
func TestInitStampsWhenTheModeChoiceIsPersisted(t *testing.T) {
	env, out, _ := testInitEnv(t, "")
	if code := initMain(context.Background(), []string{"--yes", "--no-agents"}, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	led := readLedger(t, os.Getenv("CSX_HOME"))
	if led.InitAt.IsZero() {
		t.Fatal("csx init recorded no initAt")
	}

	// Re-running init (to switch modes, say) is not a second initialization,
	// and moving the stamp would restart the clock the first-answer duration
	// is measured against.
	first := led.InitAt
	time.Sleep(1100 * time.Millisecond) // the stamp has second resolution
	var out2 bytes.Buffer
	env2 := &initEnv{
		stdin:    strings.NewReader(""),
		stdout:   &out2,
		stderr:   &out2,
		userHome: env.userHome,
		warm:     func(context.Context, io.Writer) {},
	}
	if code := initMain(context.Background(), []string{"--local-only", "--yes", "--no-agents"}, env2); code != 0 {
		t.Fatalf("second init returned %d\n%s", code, out2.String())
	}
	if again := readLedger(t, os.Getenv("CSX_HOME")).InitAt; !again.Equal(first) {
		t.Fatalf("initAt moved from %s to %s on a re-init", first, again)
	}
}

// docs/activation-funnel.md §7: the readiness rows say what they can show,
// mark an unreached stage as a gap rather than a zero, and carry the exact
// next action for the first row that is not ready. "Never seen" is also not
// "not working": a machine whose agent has never completed a handshake is not
// a machine whose MCP path is broken, and the text must not say it is.
func TestStatsPrintsTheReadinessRowsWithGapsAndNextActions(t *testing.T) {
	home := newCLIHome(t, func(cfg *config.Config) { cfg.DaemonPort = 1 })

	out, code := captureStdout(t, func() int { return Main([]string{"stats"}) })
	if code != 0 {
		t.Fatalf("stats exit = %d\n%s", code, out)
	}
	for _, want := range []string{"Readiness", "nothing here is uploaded", "csx init", "never"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats readiness block missing %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"1970-01-01", "0001-01-01", "not working"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("stats printed %q for an unreached stage:\n%s", forbidden, out)
		}
	}

	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	initAt := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	if err := db.StampFirst(context.Background(), localdb.StatInitAt, initAt); err != nil {
		t.Fatal(err)
	}
	if err := db.StampFirst(context.Background(), localdb.StatFirstHitAt, initAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	out, code = captureStdout(t, func() int { return Main([]string{"stats"}) })
	if code != 0 {
		t.Fatalf("stats exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "2026-08-20T11:00:00Z") {
		t.Errorf("stats did not print the first answer:\n%s", out)
	}
	// The one duration §5 says is computable, and only here: both endpoints
	// are local and the server holds neither in a form that survives a day.
	if !strings.Contains(out, "2h0m0s after csx init") {
		t.Errorf("stats did not print time to first answer:\n%s", out)
	}
}
