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

// The commands the #483 parser changes now read differently: options ahead
// of the command (#313), uppercase options (#315), a Windows wrapper (#339)
// and a scoped script (#356). Each still records one CLI observation the
// server takes, pass or fail.
func TestReshapedCLICommandsStillReachTheServer(t *testing.T) {
	for _, tc := range []struct {
		argv       []string
		subcommand string
	}{
		{[]string{"git", "-C", "/tmp/repo", "status"}, "status"},
		{[]string{"git", "--no-pager", "log", "-L", "1,5:main.go"}, "log"},
		{[]string{"docker", "-H", "tcp://localhost:2375", "ps"}, "ps"},
		{[]string{"gh", "-R", "cli/cli", "pr", "list"}, "pr list"},
		{[]string{"curl", "-X", "POST", "https://example.com/api"}, ""},
		{[]string{"mvn", "-DskipTests", "package"}, "package"},
		{[]string{"npm.cmd", "run", "test:unit"}, "run"},
		{[]string{`C:\nodejs\npm.cmd`, "test"}, "test"},
	} {
		for _, code := range []int{0, 1} {
			name := strings.Join(tc.argv, " ") + map[int]string{0: " pass", 1: " fail"}[code]
			t.Run(name, func(t *testing.T) {
				db, ident, cfg := testDB(t), testIdentity(t), config.Default()
				cfg.Mode = config.ModeCommunity
				rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
				res := &scanner.ScanResult{Env: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "windows", Arch: "amd64"}}
				out := CommandOutput{ToolVersion: "1.2.3", Shell: "pwsh", StartedAt: time.Now().UTC().Add(-time.Second), FinishedAt: time.Now().UTC()}
				if code != 0 {
					out.Termination = domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}
					out.Stderr = "command failed"
				}
				if err := rec.RecordCommandOutput(t.Context(), t.TempDir(), res, scanner.CommandProfile{}, tc.argv, code, out); err != nil {
					t.Fatal(err)
				}
				batches, err := (&Batcher{DB: db, Ident: ident, Cfg: cfg}).Drain(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				var cli []domain.ObservationBatch
				for _, b := range batches {
					if strings.HasPrefix(b.Package, "pkg:generic/cli/") {
						cli = append(cli, b)
					}
				}
				if len(cli) != 1 {
					t.Fatalf("CLI observations = %+v, want one", cli)
				}
				coord := domain.ParseCLICommand(tc.argv, res.Env)
				if coord.Subcommand != tc.subcommand {
					t.Errorf("subcommand = %q, want %q", coord.Subcommand, tc.subcommand)
				}
				if err := serverstore.ValidateBatch(cli[0]); err != nil {
					t.Fatalf("server refuses %q: %v (batch %+v)", cli[0].OuterCommand, err, cli[0])
				}
			})
		}
	}
}
