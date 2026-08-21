// Package config manages CSX_HOME and $CSX_HOME/config.json
// (goal.md §5.4 — every setting has a working default; the only choice
// the user ever has to make is the mode).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

// Mode values. Empty means uninitialized: csx init has not run yet.
const (
	ModeUninitialized = ""
	ModeCommunity     = "community"
	ModeLocalOnly     = "local-only"
)

// MayContactRegistries reports whether this mode is allowed to ask a public
// registry whether a package exists.
//
// LOCAL ONLY says "nothing about your projects ever leaves this machine",
// and the README says it in nine languages: local-only mode transmits
// nothing at all. A publicness probe is a transmission — it hands npm,
// PyPI, crates.io or the Go proxy the name of every dependency in the
// lockfile, one request each, from a user who chose the mode that promised
// exactly this would not happen. The daemon already refused to name
// packages to the server for this reason; the scan path, the run path,
// sample creation and the MCP server all did it anyway, on every build.
//
// Uninitialized is excluded too: before csx init, no mode has been chosen,
// so no permission has been given.
func MayContactRegistries(mode string) bool {
	return mode == ModeCommunity
}

// Config is the persisted local client configuration.
type Config struct {
	SchemaVersion    int      `json:"schemaVersion"`
	Mode             string   `json:"mode"` // "" | "community" | "local-only"
	ServerURL        string   `json:"serverUrl"`
	PeerListen       bool     `json:"peerListen"`
	PeerPort         int      `json:"peerPort"`
	DaemonPort       int      `json:"daemonPort"`
	IdleVerification string   `json:"idleVerification"`
	LLMCommand       string   `json:"llmCommand"`
	ExcludedPackages []string `json:"excludedPackages"`
	PinnedPackages   []string `json:"pinnedPackages"`
	CacheBudgetMB    int      `json:"cacheBudgetMB"`
	GithubLogin      string   `json:"githubLogin"`
	APIToken         string   `json:"apiToken"`
	AutoUpdate       string   `json:"autoUpdate"`    // auto | on | off
	UpdateChannel    string   `json:"updateChannel"` // stable (preview is explicit opt-in later)
	// FailureHook is the one switch for the build-failure lookup that
	// csx init registers with a coding agent. It is a config flag rather
	// than a registration the user must remove, because the agents do not
	// offer a per-hook disable and nobody who had to re-run an installer
	// to turn something back on ever turns it back on.
	FailureHook string `json:"failureHook"` // on | off
}

// Default returns a fresh Config with every default applied.
func Default() *Config {
	return &Config{
		SchemaVersion:    1,
		Mode:             ModeUninitialized,
		ServerURL:        "https://codesamplex.dev",
		PeerListen:       false,
		PeerPort:         48620,
		DaemonPort:       48619,
		IdleVerification: "off",
		ExcludedPackages: []string{},
		PinnedPackages:   []string{},
		CacheBudgetMB:    512,
		AutoUpdate:       "auto",
		UpdateChannel:    "stable",
		FailureHook:      "on",
	}
}

// Home resolves the csx home directory: $CSX_HOME if set, else ~/.csx.
func Home() (string, error) {
	if h := os.Getenv("CSX_HOME"); h != "" {
		return h, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve home: %w", err)
	}
	return filepath.Join(userHome, ".csx"), nil
}

// EnsureHome creates the home directory tree (cas, samples, logs).
// It is idempotent.
func EnsureHome(home string) error {
	for _, sub := range []string{"", "cas", "samples", "logs"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o700); err != nil {
			return fmt.Errorf("config: ensure home: %w", err)
		}
	}
	return nil
}

// Load reads home/config.json. A missing file yields the defaults;
// fields absent from the file keep their default values.
func Load(home string) (*Config, error) {
	c := Default()
	raw, err := os.ReadFile(filepath.Join(home, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: load: %w", err)
	}
	if err := json.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("config: parse config.json: %w", err)
	}
	if c.ExcludedPackages == nil {
		c.ExcludedPackages = []string{}
	}
	if c.PinnedPackages == nil {
		c.PinnedPackages = []string{}
	}
	return c, nil
}

// Save writes home/config.json with mode 0600 (it may hold apiToken).
func (c *Config) Save(home string) error {
	return csxupdate.WithLock(home, func() error { return c.saveUnlocked(home) })
}

func (c *Config) saveUnlocked(home string) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("config: save: %w", err)
	}
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("config: save: %w", err)
	}
	return nil
}
