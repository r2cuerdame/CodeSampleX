package cli

import (
	"context"
	"path/filepath"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// stampActivation writes one local activation stamp
// (docs/activation-funnel.md §7) and reports nothing when it cannot.
//
// Every failure here is silent on purpose. These stamps exist to answer "is
// this install working"; a broken ledger must never be the reason a command
// that was working stops. §7's rule is that a failure stays visible as an
// UNMEASURED stage — the panel prints an em dash — rather than being repaired
// later with a timestamp that would misdate the stage.
//
// Nothing written here leaves the machine in any mode. S1 and S2 happen
// before `csx init` asks the mode question, so transmitting them would be
// collecting before consent exists (§2.3).
//
// It opens the local store, which `csx version` and `csx --help` did not do
// before. Measured on a warm home on a Windows workstation: 12.5 ms per
// open+stamp+close, against 100–200 ms of process startup for the same
// commands. Every other csx command already pays this open, so the cost is
// the same one, not a new one.
func stampActivation(ctx context.Context, key string) {
	home, err := config.Home()
	if err != nil {
		return
	}
	if err := config.EnsureHome(home); err != nil {
		return
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		return
	}
	defer db.Close() //nolint:errcheck // a stamp is best effort by contract
	_ = db.StampFirst(ctx, key, time.Now().UTC())
}
