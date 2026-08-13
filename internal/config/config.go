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
)

// Mode values. Empty means uninitialized: csx init has not run yet.
const (
	ModeUninitialized = ""
	ModeCommunity     = "community"
	ModeLocalOnly     = "local-only"
)

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
