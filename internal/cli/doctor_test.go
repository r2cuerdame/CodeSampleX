package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/doctor"
	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

func setupTestDoctorEnv(t *testing.T, mutate func(*config.Config)) (string, string) {
	t.Helper()
	home := newCLIHome(t, mutate)
	root := t.TempDir()
	agentHome := t.TempDir()

	t.Setenv("CSX_LAUNCHER_ROOT", root)
	t.Setenv("CSX_AGENT_HOME", agentHome)

	exeName := "csx"
	if runtime.GOOS == "windows" {
		exeName = "csx.exe"
	}
	// Create launcher
	_ = os.WriteFile(filepath.Join(root, exeName), []byte("launcher"), 0o755)

	// Create payload
	payloadPath, _ := launcher.PayloadPath(root, "v1.0.0")
	_ = os.MkdirAll(filepath.Dir(payloadPath), 0o700)
	pBytes := []byte("payload")
	_ = os.WriteFile(payloadPath, pBytes, 0o700)
	pSum := sha256.Sum256(pBytes)

	// Write active.json
	act := launcher.Active{
		Schema:  1,
		Current: launcher.Descriptor{Version: "v1.0.0", SHA256: hex.EncodeToString(pSum[:]), Sequence: 1},
	}
	rawAct, _ := json.Marshal(act)
	_ = os.WriteFile(launcher.Path(root), rawAct, 0o600)

	doctorContextFactory = func(fix, verbose bool) (*doctor.CheckContext, error) {
		cctx, err := doctor.DefaultCheckContext(fix, verbose)
		if err != nil {
			return nil, err
		}
		cctx.Home = home
		cctx.Root = root
		cctx.AgentHome = agentHome
		cctx.CommandRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "--launcher-version" {
				return []byte("csx-launcher v1.0.0\n"), nil
			}
			if len(args) > 0 && (args[0] == "version" || (len(args) > 1 && args[1] == "--path")) {
				return []byte("csx v1.0.0\n"), nil
			}
			return []byte("ok"), nil
		}
		return cctx, nil
	}
	t.Cleanup(func() { doctorContextFactory = doctor.DefaultCheckContext })

	return home, root
}

func TestDoctorHelp(t *testing.T) {
	out, code := captureStdout(t, func() int {
		return Main([]string{"doctor", "--help"})
	})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(out, "usage: csx doctor") {
		t.Fatalf("expected usage text, got:\n%s", out)
	}
}

func TestDoctorUnknownOption(t *testing.T) {
	out, code := captureStdout(t, func() int {
		return Main([]string{"doctor", "--bogus-flag"})
	})
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(out, "unknown option") {
		t.Fatalf("expected unknown option error, got:\n%s", out)
	}
}

func TestDoctorHealthyTable(t *testing.T) {
	setupTestDoctorEnv(t, nil)

	out, code := captureStdout(t, func() int {
		return Main([]string{"doctor"})
	})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "CHECK") || !strings.Contains(out, "TIER") || !strings.Contains(out, "STATUS") {
		t.Fatalf("expected table header, got:\n%s", out)
	}
	if !strings.Contains(out, "Overall status: HEALTHY") {
		t.Fatalf("expected HEALTHY status, got:\n%s", out)
	}
}

func TestDoctorJSONAndRedaction(t *testing.T) {
	secretTok := "csx_test_secret_token_12345678"
	setupTestDoctorEnv(t, func(c *config.Config) {
		c.APIToken = secretTok
		c.GithubLogin = "octocat"
	})

	out, code := captureStdout(t, func() int {
		return Main([]string{"doctor", "--json"})
	})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d\noutput:\n%s", code, out)
	}

	var res doctor.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, out)
	}
	if !res.Healthy {
		t.Fatalf("expected healthy true, got false")
	}

	// Verify ZERO secret leakage
	if strings.Contains(out, secretTok) || strings.Contains(out, "test_secret_token") {
		t.Fatalf("SECRET LEAKED in JSON output:\n%s", out)
	}
}

func TestDoctorVerbose(t *testing.T) {
	setupTestDoctorEnv(t, nil)

	out, code := captureStdout(t, func() int {
		return Main([]string{"doctor", "--verbose"})
	})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "Summary:") {
		t.Fatalf("expected Summary line, got:\n%s", out)
	}
}

func TestDoctorFix(t *testing.T) {
	home, _ := setupTestDoctorEnv(t, nil)

	// Inject a fixable stale lock
	_ = os.WriteFile(filepath.Join(home, "daemon.lock"), []byte("99999\n"), 0o600)

	out, code := captureStdout(t, func() int {
		return Main([]string{"doctor", "--fix"})
	})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d\noutput:\n%s", code, out)
	}

	// Verify the lock was removed
	if _, err := os.Stat(filepath.Join(home, "daemon.lock")); !os.IsNotExist(err) {
		t.Fatal("expected stale daemon.lock to be removed by --fix")
	}
}

func TestDoctorUnhealthyExitCode1(t *testing.T) {
	_, root := setupTestDoctorEnv(t, nil)

	// Remove launcher executable so LauncherIntegrityCheck fails
	exeName := "csx"
	if runtime.GOOS == "windows" {
		exeName = "csx.exe"
	}
	_ = os.Remove(filepath.Join(root, exeName))

	// Doctor should exit with code 1
	_, code := captureStdout(t, func() int {
		return Main([]string{"doctor"})
	})
	if code != 1 {
		t.Fatalf("expected exit code 1 for unhealthy status, got %d", code)
	}
}
