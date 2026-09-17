package evidence

import (
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A command the farm runs to fill a CLI gap is recorded with farm provenance
// -- the "farm:" symbol prefix the recall layer already reads -- and the
// resulting batch, PASS or FAIL, is one the server admits. Farm provenance
// used to be stamped into actualToolchain as well, which made every farm
// FAIL row's fingerprint disagree with the server's recomputation and
// refused it on arrival.
func TestFarmProvenanceIsRecordedAndAdmissible(t *testing.T) {
	for _, code := range []int{0, 128} {
		db, ident, cfg := testDB(t), testIdentity(t), config.Default()
		cfg.Mode = config.ModeCommunity
		rec := &Recorder{DB: db, Ident: ident, Cfg: cfg, CLIProvenance: domain.ProvenanceFarm}
		out := CommandOutput{ToolVersion: "2.47.2", Shell: "direct", StartedAt: time.Now().UTC().Add(-time.Second), FinishedAt: time.Now().UTC()}
		if code != 0 {
			out.Termination = domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}
			out.Stderr = "fatal: not a git repository (or any of the parent directories): .git"
		}
		if err := rec.RecordCommandOutput(t.Context(), t.TempDir(), nil, scanner.CommandProfile{}, []string{"git", "status", "--short"}, code, out); err != nil {
			t.Fatal(err)
		}
		batches, err := (&Batcher{DB: db, Ident: ident, Cfg: cfg}).Drain(t.Context())
		if err != nil || len(batches) != 1 {
			t.Fatalf("exit %d: batches=%+v err=%v", code, batches, err)
		}
		b := batches[0]
		if b.Package != "pkg:generic/cli/git@2.47.2" || b.Symbol != "farm:status --short" {
			t.Fatalf("exit %d: farm provenance not recorded: %+v", code, b)
		}
		if err := serverstore.ValidateBatch(b); err != nil {
			t.Fatalf("exit %d: the server refuses the farm batch: %v (%+v)", code, err, b)
		}
		if _, _, prov := domain.DecodeCLISymbol(b.Symbol, "git", b.Environment); prov != domain.ProvenanceFarm {
			t.Fatalf("exit %d: provenance decodes as %q", code, prov)
		}
		if strings.Contains(b.ActualToolchain, "farm") {
			t.Fatalf("exit %d: provenance leaked into actualToolchain: %+v", code, b)
		}
	}
}
