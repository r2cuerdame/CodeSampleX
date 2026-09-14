package evidence

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Production accepted package evidence from npm ls but refused its companion
// CLI observation with "projectBucket is empty", repeatedly degrading health.
func TestCommandOutputDeliversItsCLIObservation(t *testing.T) {
	for _, tc := range []struct {
		tool, version, ecosystem, runtime string
		args                              []string
	}{
		{"npm", "10.9.8", "npm", "node", []string{"ls"}},
		{"go", "1.25.0", "golang", "go", []string{"mod", "verify"}},
		{"uv", "0.8.0", "pypi", "python", []string{"pip", "check"}},
		{"cargo", "1.90.0", "cargo", "rust", []string{"check"}},
		{"python3", "3.13.0", "pypi", "python", []string{"-m", "pytest"}},
		{"mvnw", "3.9.0", "maven", "java", []string{"test"}},
		{"npm", "", "npm", "node", []string{"ls"}},
	} {
		for _, code := range []int{0, 1, 2, 3} {
			label := tc.tool + "/" + tc.version + map[int]string{0: "/pass", 1: "/fail", 2: "/fail-without-diagnostic", 3: "/exit-code-without-termination-object"}[code]
			t.Run(label, func(t *testing.T) {
				db, ident, cfg := testDB(t), testIdentity(t), config.Default()
				cfg.Mode = config.ModeCommunity
				rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
				res := &scanner.ScanResult{Env: domain.EnvironmentFingerprint{
					SchemaVersion: 1, Ecosystem: tc.ecosystem, OS: "linux", Arch: "amd64", Runtime: tc.runtime,
				}}
				out := CommandOutput{ToolVersion: tc.version, Shell: "bash", StartedAt: time.Now().UTC().Add(-time.Second), FinishedAt: time.Now().UTC()}
				if code != 0 {
					out.Termination = domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}
					out.Stderr = "command failed"
					if code == 2 {
						out.Stderr = ""
					}
					if code == 3 {
						out.Termination = domain.FailureTermination{}
					}
				}
				argv := append([]string{tc.tool}, tc.args...)
				dir := t.TempDir()
				if err := rec.RecordCommandOutput(t.Context(), dir, res, scanner.CommandProfile{}, argv, code, out); err != nil {
					t.Fatal(err)
				}
				batches, err := (&Batcher{DB: db, Ident: ident, Cfg: cfg}).Drain(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if len(batches) != 1 || !strings.HasPrefix(batches[0].Package, "pkg:generic/cli/") {
					t.Fatalf("missing CLI companion observation: %+v", batches)
				}
				abs, _ := filepath.Abs(dir)
				if want := ident.ProjectBucket(abs, time.Now().UTC().Format("2006-01")); batches[0].ProjectBucket != want {
					t.Fatalf("CLI bucket is not the actual working directory's rotating HMAC")
				}
				f := serverstore.NewFake()
				accepted, rejected, err := f.IngestBatches(t.Context(), batches)
				if err != nil || accepted != 1 || len(rejected) != 0 {
					t.Fatalf("CLI companion cannot reach the server: accepted=%d rejected=%+v err=%v batch=%+v", accepted, rejected, err, batches[0])
				}
			})
		}
	}
}

func TestCommandOutsideAProjectStillDeliversMeasuredHostEvidence(t *testing.T) {
	db, ident, cfg := testDB(t), testIdentity(t), config.Default()
	cfg.Mode = config.ModeCommunity
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	if err := rec.RecordCommandOutput(t.Context(), t.TempDir(), nil, scanner.CommandProfile{}, []string{"git", "status"}, 0, CommandOutput{ToolVersion: "2.51.0"}); err != nil {
		t.Fatal(err)
	}
	batches, err := (&Batcher{DB: db, Ident: ident, Cfg: cfg}).Drain(t.Context())
	if err != nil || len(batches) != 1 {
		t.Fatalf("batches=%+v err=%v", batches, err)
	}
	if env := batches[0].Environment; env.OS != runtime.GOOS || env.Arch != runtime.GOARCH || env.Ecosystem != "generic" {
		t.Fatalf("missing measured host facts: %+v", env)
	}
	if err := serverstore.ValidateBatch(batches[0]); err != nil {
		t.Fatal(err)
	}
}
