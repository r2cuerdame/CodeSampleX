package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHomeEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CSX_HOME", dir)
	home, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if home != dir {
		t.Fatalf("Home = %q, want %q", home, dir)
	}
}

func TestHomeDefault(t *testing.T) {
	t.Setenv("CSX_HOME", "")
	home, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(userHome, ".csx")
	if home != want {
		t.Fatalf("Home = %q, want %q", home, want)
	}
}

func TestDefaults(t *testing.T) {
	c := Default()
	if c.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", c.SchemaVersion)
	}
	if c.Mode != "" {
		t.Errorf("Mode = %q, want empty (uninitialized)", c.Mode)
	}
	if c.ServerURL != "https://codesamplex.dev" {
		t.Errorf("ServerURL = %q", c.ServerURL)
	}
	if c.PeerListen {
		t.Error("PeerListen default must be false")
	}
	if c.PeerPort != 48620 {
		t.Errorf("PeerPort = %d, want 48620", c.PeerPort)
	}
	if c.DaemonPort != 48619 {
		t.Errorf("DaemonPort = %d, want 48619", c.DaemonPort)
	}
	if c.IdleVerification != "off" {
		t.Errorf("IdleVerification = %q, want %q", c.IdleVerification, "off")
	}
	if c.LLMCommand != "" {
		t.Errorf("LLMCommand = %q, want empty", c.LLMCommand)
	}
	if c.ExcludedPackages == nil || len(c.ExcludedPackages) != 0 {
		t.Errorf("ExcludedPackages = %#v, want empty non-nil", c.ExcludedPackages)
	}
	if c.PinnedPackages == nil || len(c.PinnedPackages) != 0 {
		t.Errorf("PinnedPackages = %#v, want empty non-nil", c.PinnedPackages)
	}
	if c.CacheBudgetMB != 512 {
		t.Errorf("CacheBudgetMB = %d, want 512", c.CacheBudgetMB)
	}
	if c.GithubLogin != "" || c.APIToken != "" {
		t.Error("GithubLogin/APIToken must default empty")
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	home := t.TempDir()
	c, err := Load(home)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, _ := json.Marshal(c)
	want, _ := json.Marshal(Default())
	if string(got) != string(want) {
		t.Fatalf("Load on missing file = %s, want defaults %s", got, want)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	c := Default()
	c.Mode = "community"
	c.PeerListen = true
	c.ExcludedPackages = []string{"pkg:npm/leftpad@*"}
	c.PinnedPackages = []string{"pkg:npm/axios@1.12.0"}
	c.GithubLogin = "octocat"
	c.APIToken = "tok123"
	if err := c.Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(home)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(c)
	if string(g) != string(w) {
		t.Fatalf("round-trip mismatch:\n got %s\nwant %s", g, w)
	}
}

func TestSaveFileModeAndJSONFieldNames(t *testing.T) {
	home := t.TempDir()
	if err := Default().Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(home, "config.json")
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("config.json mode = %o, want 600", perm)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{
		"schemaVersion", "mode", "serverUrl", "peerListen", "peerPort",
		"daemonPort", "idleVerification", "llmCommand", "excludedPackages",
		"pinnedPackages", "cacheBudgetMB", "githubLogin", "apiToken",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("saved config missing JSON field %q; got keys %v", key, m)
		}
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	home := t.TempDir()
	partial := []byte(`{"schemaVersion":1,"mode":"local-only"}`)
	if err := os.WriteFile(filepath.Join(home, "config.json"), partial, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(home)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Mode != "local-only" {
		t.Errorf("Mode = %q, want local-only", c.Mode)
	}
	if c.ServerURL != "https://codesamplex.dev" {
		t.Errorf("ServerURL = %q, want default preserved", c.ServerURL)
	}
	if c.CacheBudgetMB != 512 {
		t.Errorf("CacheBudgetMB = %d, want default 512", c.CacheBudgetMB)
	}
}

func TestLoadCorruptFileErrors(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("Load on corrupt file: want error, got nil")
	}
}

func TestEnsureHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "csxhome")
	if err := EnsureHome(home); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	for _, sub := range []string{"", "cas", "samples", "logs"} {
		p := filepath.Join(home, sub)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat %s: %v", p, err)
		}
		if !fi.IsDir() {
			t.Errorf("%s is not a directory", p)
		}
	}
	// Idempotent.
	if err := EnsureHome(home); err != nil {
		t.Fatalf("EnsureHome (second): %v", err)
	}
}
