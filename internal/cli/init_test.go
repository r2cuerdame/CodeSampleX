package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

// contract54 is an independent copy of the goal.md §5.4 block. The
// embedded contract screen must contain it verbatim.
const contract54 = `CodeSampleX Community

You get
✓ Public compatibility knowledge
✓ Verified code answers
✓ Local agent integration
✓ Public sample cache

You contribute
✓ Public package/version usage
✓ Public API/symbol usage when detectable
✓ Build/typecheck/test result
✓ Sanitized failure fingerprints

Never shared automatically
✕ Source code
✕ Repository/project name
✕ File names or paths
✕ Source snippets
✕ Secrets or environment variables
✕ Private packages
✕ Raw compiler/runtime logs

[ JOIN COMMUNITY ]    [ LOCAL ONLY ]`

func testInitEnv(t *testing.T, stdin string) (*initEnv, *bytes.Buffer, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	userHome := t.TempDir()
	var out bytes.Buffer
	env := &initEnv{
		stdin:    strings.NewReader(stdin),
		stdout:   &out,
		stderr:   &out,
		userHome: func() (string, error) { return userHome, nil },
	}
	return env, &out, userHome
}

func TestInitContractTextVerbatim(t *testing.T) {
	if !strings.Contains(contractText, contract54) {
		t.Fatalf("embedded contract screen does not contain the §5.4 block verbatim:\n%s", contractText)
	}
}

func TestInitYesNonInteractive(t *testing.T) {
	env, out, _ := testInitEnv(t, "") // empty stdin: must never block or prompt
	if code := initMain(context.Background(), []string{"--yes"}, env); code != 0 {
		t.Fatalf("init --yes returned %d, want 0\n%s", code, out.String())
	}
	home := os.Getenv("CSX_HOME")
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeCommunity {
		t.Fatalf("mode = %q, want community", cfg.Mode)
	}
	if _, err := os.Stat(filepath.Join(home, "identity.json")); err != nil {
		t.Fatalf("identity not created: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "ed25519:") {
		t.Errorf("output missing peer ID:\n%s", s)
	}
	if !strings.Contains(s, home) {
		t.Errorf("output missing home path:\n%s", s)
	}
	if !strings.Contains(s, "irm https://codesamplex.dev/install.ps1 | iex") {
		t.Errorf("output missing other-machine one-liner:\n%s", s)
	}
	if strings.Contains(s, "Choose [community/local-only]:") {
		t.Errorf("--yes must not prompt:\n%s", s)
	}
}

func TestInitInteractiveCommunity(t *testing.T) {
	env, out, _ := testInitEnv(t, "community\n")
	if code := initMain(context.Background(), nil, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, contract54) {
		t.Errorf("interactive init did not print the contract screen:\n%s", s)
	}
	if !strings.Contains(s, "Choose [community/local-only]: ") {
		t.Errorf("missing prompt:\n%s", s)
	}
	cfg, err := config.Load(os.Getenv("CSX_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeCommunity {
		t.Fatalf("mode = %q, want community", cfg.Mode)
	}
}

func TestInitInteractiveRetryThenLocalOnly(t *testing.T) {
	env, out, _ := testInitEnv(t, "whatever\nlocal-only\n")
	if code := initMain(context.Background(), nil, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	cfg, err := config.Load(os.Getenv("CSX_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeLocalOnly {
		t.Fatalf("mode = %q, want local-only", cfg.Mode)
	}
}

func TestInitLocalOnlyFlagWritesLocalOnly(t *testing.T) {
	env, out, _ := testInitEnv(t, "")
	if code := initMain(context.Background(), []string{"--local-only", "--yes"}, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	cfg, err := config.Load(os.Getenv("CSX_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeLocalOnly {
		t.Fatalf("mode = %q, want local-only", cfg.Mode)
	}
}

// Agent integration is mode-independent: local-only users still get the
// MCP server registered for their agents.
func TestInitLocalOnlyStillInstallsAgents(t *testing.T) {
	env, out, userHome := testInitEnv(t, "")
	if err := os.MkdirAll(filepath.Join(userHome, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := initMain(context.Background(), []string{"--local-only", "--yes"}, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	raw, err := os.ReadFile(filepath.Join(userHome, ".claude.json"))
	if err != nil {
		t.Fatalf("claude MCP registration missing: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	servers, _ := m["mcpServers"].(map[string]any)
	if _, ok := servers["csx"]; !ok {
		t.Fatalf("mcpServers.csx missing in %s", raw)
	}
}

func TestInitInteractiveAgentConfirmDeclined(t *testing.T) {
	// Mode via flag, so the only stdin interaction is the per-agent y/N;
	// "n" must skip the install.
	env, out, userHome := testInitEnv(t, "n\n")
	if err := os.MkdirAll(filepath.Join(userHome, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := initMain(context.Background(), []string{"--community"}, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(userHome, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf(".claude.json should not exist after declining, stat err=%v", err)
	}
	if !strings.Contains(out.String(), "skipped") {
		t.Errorf("summary should mention the skip:\n%s", out.String())
	}
}

func TestInitServerOverride(t *testing.T) {
	env, out, _ := testInitEnv(t, "")
	if code := initMain(context.Background(), []string{"--yes", "--server", "http://127.0.0.1:1"}, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	cfg, err := config.Load(os.Getenv("CSX_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != "http://127.0.0.1:1" {
		t.Fatalf("serverUrl = %q", cfg.ServerURL)
	}
}

func TestInitConflictingModeFlags(t *testing.T) {
	env, _, _ := testInitEnv(t, "")
	if code := initMain(context.Background(), []string{"--community", "--local-only"}, env); code != 2 {
		t.Fatalf("conflicting flags returned %d, want 2", code)
	}
}

func TestInitCommandRegistered(t *testing.T) {
	for _, c := range Commands() {
		if c.Name == "init" {
			return
		}
	}
	t.Fatal("init command not registered")
}
