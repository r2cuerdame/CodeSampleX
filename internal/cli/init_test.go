package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
	// Never inherit a developer's real override: tests that want one set
	// it explicitly after this call.
	t.Setenv("CSX_AGENT_HOME", "")
	userHome := t.TempDir()
	var out bytes.Buffer
	env := &initEnv{
		stdin:    strings.NewReader(stdin),
		stdout:   &out,
		stderr:   &out,
		userHome: func() (string, error) { return userHome, nil },
		warm:     func(context.Context, io.Writer) {},
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
	if strings.Contains(s, "Choose [1/2]") {
		t.Errorf("--yes must not prompt:\n%s", s)
	}
}

func TestInitStartsBackgroundSync(t *testing.T) {
	env, out, _ := testInitEnv(t, "")
	called := false
	env.startDaemon = func(context.Context) error {
		called = true
		return nil
	}
	if code := initMain(context.Background(), []string{"--yes"}, env); code != 0 {
		t.Fatalf("init --yes returned %d\n%s", code, out.String())
	}
	if !called {
		t.Fatal("init did not start the background sync daemon")
	}
	if !strings.Contains(out.String(), "background sync running") {
		t.Errorf("init hid daemon status:\n%s", out.String())
	}
}

func TestInitLocalOnlyDoesNotStartBackgroundSync(t *testing.T) {
	env, out, _ := testInitEnv(t, "")
	called := false
	env.startDaemon = func(context.Context) error {
		called = true
		return nil
	}
	if code := initMain(context.Background(), []string{"--local-only", "--yes"}, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	if called {
		t.Fatal("local-only init started a network-capable daemon")
	}
}

func TestInitCommunityToLocalOnlyStopsDaemonBeforeSaving(t *testing.T) {
	env, out, _ := testInitEnv(t, "")
	home := os.Getenv("CSX_HOME")
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}

	stopped := false
	env.stopDaemon = func(context.Context) error {
		current, err := config.Load(home)
		if err != nil {
			return err
		}
		if current.Mode != config.ModeCommunity {
			t.Fatalf("mode was saved as %q before the old daemon stopped", current.Mode)
		}
		stopped = true
		return nil
	}
	if code := initMain(context.Background(), []string{"--local-only", "--yes"}, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	if !stopped {
		t.Fatal("community daemon was not stopped during privacy downshift")
	}
	cfg, err := config.Load(home)
	if err != nil || cfg.Mode != config.ModeLocalOnly {
		t.Fatalf("mode after init = %q err=%v", cfg.Mode, err)
	}
}

func TestInitInteractiveCommunity(t *testing.T) {
	// "1" — the single keystroke the prompt advertises.
	env, out, _ := testInitEnv(t, "1\n")
	if code := initMain(context.Background(), nil, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, contract54) {
		t.Errorf("interactive init did not print the contract screen:\n%s", s)
	}
	if !strings.Contains(s, "Choose [1/2] (default 1): ") {
		t.Errorf("missing prompt:\n%s", s)
	}
	if !strings.Contains(s, "1) JOIN COMMUNITY") || !strings.Contains(s, "2) LOCAL ONLY") {
		t.Errorf("numbered options missing:\n%s", s)
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
	env, out, _ := testInitEnv(t, "whatever\n2\n")
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

// Empty input (just Enter) takes the advertised default, and the old
// spelled-out answers keep working for anyone with them in a script.
func TestInitInteractiveAcceptsDefaultAndWords(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\n", config.ModeCommunity},
		{"community\n", config.ModeCommunity},
		{"local-only\n", config.ModeLocalOnly},
		{"l\n", config.ModeLocalOnly},
	} {
		env, out, _ := testInitEnv(t, tc.in)
		if code := initMain(context.Background(), nil, env); code != 0 {
			t.Fatalf("input %q: init returned %d\n%s", tc.in, code, out.String())
		}
		cfg, err := config.Load(os.Getenv("CSX_HOME"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Mode != tc.want {
			t.Errorf("input %q: mode = %q, want %q", tc.in, cfg.Mode, tc.want)
		}
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

// filesUnder lists every regular file below root, relative to root.
func filesUnder(t *testing.T, root string) []string {
	t.Helper()
	var got []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		got = append(got, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	return got
}

// Regression (host pollution): `csx init --community --yes --no-agents`
// must do config + identity and touch NO agent home at all — the e2e
// harness runs it on every execution and used to dirty the developer's
// real ~/.claude.json, ~/.codex, ~/.gemini.
func TestInitNoAgentsSkipsAllAgentIntegration(t *testing.T) {
	env, out, userHome := testInitEnv(t, "")
	agentHome := t.TempDir()
	t.Setenv("CSX_AGENT_HOME", agentHome)
	// Agents are detectable in BOTH candidate homes: only --no-agents can
	// stop csx from writing into them.
	plantDir(t, userHome, ".claude")
	plantDir(t, agentHome, ".claude")
	plantDir(t, agentHome, ".codex")

	if code := initMain(context.Background(), []string{"--community", "--yes", "--no-agents"}, env); code != 0 {
		t.Fatalf("init --no-agents returned %d\n%s", code, out.String())
	}

	// Config + identity still happen.
	home := os.Getenv("CSX_HOME")
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeCommunity {
		t.Fatalf("mode = %q, want community", cfg.Mode)
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); err != nil {
		t.Fatalf("config.json not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "identity.json")); err != nil {
		t.Fatalf("identity not created: %v", err)
	}

	// Neither home received a single file.
	if got := filesUnder(t, agentHome); len(got) != 0 {
		t.Errorf("--no-agents wrote into the agent home: %v", got)
	}
	if got := filesUnder(t, userHome); len(got) != 0 {
		t.Errorf("--no-agents wrote into the user home: %v", got)
	}

	// The summary must say what was skipped and how to do it later.
	s := out.String()
	if !strings.Contains(s, "--no-agents") || !strings.Contains(s, "MCP registration") {
		t.Errorf("summary does not state that MCP registration was skipped:\n%s", s)
	}
	if !strings.Contains(s, "csx init") {
		t.Errorf("summary does not say how to install agent integration later:\n%s", s)
	}
}

// CSX_AGENT_HOME confines every agent path csx writes; the OS user home
// seam is not consulted when it is set.
func TestInitAgentHomeEnvConfinesEveryWrite(t *testing.T) {
	env, out, userHome := testInitEnv(t, "")
	agentHome := t.TempDir()
	t.Setenv("CSX_AGENT_HOME", agentHome)
	plantDir(t, userHome, ".claude") // would be written to without the override
	plantDir(t, userHome, ".codex")
	plantDir(t, agentHome, ".claude")
	plantDir(t, agentHome, ".codex")

	if code := initMain(context.Background(), []string{"--community", "--yes"}, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}

	if got := filesUnder(t, userHome); len(got) != 0 {
		t.Fatalf("CSX_AGENT_HOME ignored: wrote outside it into %s: %v", userHome, got)
	}
	got := filesUnder(t, agentHome)
	for _, want := range []string{".claude.json", ".claude/CLAUDE.md", ".codex/config.toml", ".codex/AGENTS.md"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %s under the agent home, got %v", want, got)
		}
	}
	// Every path reported in the summary lives under the agent home.
	for _, line := range strings.Split(out.String(), "\n") {
		i := strings.Index(line, "→ ")
		if i < 0 {
			continue
		}
		p := strings.TrimSpace(line[i+len("→ "):])
		if !strings.HasPrefix(p, agentHome) {
			t.Errorf("install wrote outside CSX_AGENT_HOME: %s", p)
		}
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

// The product default is community mode even when stdin is unavailable.
// Piped installers consume stdin before init runs, so EOF must take the same
// advertised default as pressing Enter. --local-only remains the explicit
// opt-out and is covered separately.
func TestInitWithoutATerminalUsesCommunityDefault(t *testing.T) {
	env, out, _ := testInitEnv(t, "") // empty stdin == EOF, as through a pipe
	if code := initMain(context.Background(), nil, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	cfg, err := config.Load(os.Getenv("CSX_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeCommunity {
		t.Fatalf("mode = %q with no answer given, want community default", cfg.Mode)
	}
	s := out.String()
	if !strings.Contains(s, "default: COMMUNITY") {
		t.Errorf("the user must be told which default was selected:\n%s", s)
	}
	if !strings.Contains(s, "csx init --local-only") {
		t.Errorf("the user must be told how to opt out:\n%s", s)
	}
}

// An explicit Enter is a human taking the default — the answer the EOF
// branch used to steal. It must still mean community.
func TestInitBareEnterStillJoins(t *testing.T) {
	env, out, _ := testInitEnv(t, "\n")
	if code := initMain(context.Background(), nil, env); code != 0 {
		t.Fatalf("init returned %d\n%s", code, out.String())
	}
	cfg, err := config.Load(os.Getenv("CSX_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeCommunity {
		t.Fatalf("mode = %q, want community when the user pressed Enter", cfg.Mode)
	}
}
