package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Codex gets the lookup too. Its hook is declared in config.toml rather than a
// JSON settings file, it has no failure-only event, and — measured, not read —
// it will not run a command whose path is quoted around a space.
func TestInstallingCodexRegistersTheFailureHook(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".codex")

	if _, err := installCodex(home); err != nil {
		t.Fatal(err)
	}

	toml := readFile(t, filepath.Join(home, ".codex", "config.toml"))
	for _, want := range []string{
		"[[hooks.PostToolUse]]", // Codex has no PostToolUseFailure
		`matcher = "^Bash$"`,
		`type = "command"`,
		"hook agent",
		"timeout =",
	} {
		if !strings.Contains(toml, want) {
			t.Errorf("config.toml is missing %q:\n%s", want, toml)
		}
	}
	// The MCP registration is not replaced by the hook; both live in our block.
	if !strings.Contains(toml, "[mcp_servers.csx]") {
		t.Errorf("the hook install dropped the MCP server:\n%s", toml)
	}
}

// Every update re-runs the installer. Our block is marker-fenced, so a second
// run must leave exactly one of each.
func TestInstallingCodexTwiceRegistersOneHook(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".codex")

	if _, err := installCodex(home); err != nil {
		t.Fatal(err)
	}
	if _, err := installCodex(home); err != nil {
		t.Fatal(err)
	}

	toml := readFile(t, filepath.Join(home, ".codex", "config.toml"))
	if n := strings.Count(toml, "[[hooks.PostToolUse]]"); n != 1 {
		t.Errorf("registered %d hooks, want 1:\n%s", n, toml)
	}
	if n := strings.Count(toml, "[mcp_servers.csx]"); n != 1 {
		t.Errorf("registered %d MCP servers, want 1", n)
	}
}

// A path with a space in it is the ordinary case on Windows — the install
// lands under C:\Users\<name>\AppData\Local, and plenty of names have a space.
// Codex runs the command as a shell-ish string and quoting does not save it:
// measured, a quoted path with a space simply never ran, while the same file
// reached through its 8.3 short name did.
//
// So the command written into config.toml must never contain a space. If we
// cannot produce one that does not, we must not pretend we registered a
// working hook.
func TestCodexHookCommandNeverContainsASpace(t *testing.T) {
	// The path has to EXIST. GetShortPathName asks the filesystem for the 8.3
	// name, so a made-up path fails the sizing call, spaceFreePath reports
	// "no space-free form", and the loop below skips the one case this test
	// exists for — on Windows and, through the plain guard, everywhere else.
	// The conversion had zero coverage on any platform.
	spaced := filepath.Join(t.TempDir(), "John Smith", "csx")
	if err := os.MkdirAll(filepath.Dir(spaced), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spaced, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(t.TempDir(), "csx")
	if err := os.WriteFile(plain, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}

	sawSpaced := false
	for _, exe := range []string{spaced, plain} {
		got, ok := codexHookCommand(exe)
		if !ok {
			// Refusing is allowed — a volume with 8.3 names disabled has no
			// space-free form. Registering a command that cannot run is not.
			continue
		}
		if strings.Contains(exe, " ") {
			sawSpaced = true
		}
		if strings.ContainsAny(strings.TrimSuffix(got, " hook agent"), " ") {
			t.Errorf("codexHookCommand(%q) = %q, which Codex will not run", exe, got)
		}
		if !strings.HasSuffix(got, "hook agent") {
			t.Errorf("codexHookCommand(%q) = %q, want it to call the hook", exe, got)
		}
	}
	if runtime.GOOS == "windows" && !sawSpaced {
		t.Skip("this volume has 8.3 short names disabled, so there is no space-free form to check")
	}
}
