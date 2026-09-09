package doctor

import (
	"context"
	"net/http"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

// Tier categorizes a diagnostic check's operational severity.
type Tier string

const (
	// TierP0 represents critical operational invariants (launcher, active payload,
	// database integrity, server compatibility, MCP viability). If any P0 check
	// fails, csx cannot operate correctly.
	TierP0 Tier = "P0"

	// TierP1 represents operational hygiene, orphaned resources, stale locks,
	// cache payloads, agent configs, session validity, and registry reachability.
	TierP1 Tier = "P1"
)

// Status represents the outcome of a diagnostic check.
type Status string

const (
	StatusPass  Status = "PASS"
	StatusWarn  Status = "WARN"
	StatusFail  Status = "FAIL"
	StatusFixed Status = "FIXED"
)

// Diagnosis records the outcome of a single check.
type Diagnosis struct {
	ID          string   `json:"id"`
	Tier        Tier     `json:"tier"`
	Status      Status   `json:"status"`
	Summary     string   `json:"summary"`
	Details     []string `json:"details,omitempty"`
	Fixable     bool     `json:"fixable"`
	Remediation string   `json:"remediation,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// ProcessInfo describes a running process for orphan detection.
type ProcessInfo struct {
	Pid       int    `json:"pid"`
	ParentPid int    `json:"parentPid"`
	Name      string `json:"name"`
	Cmdline   string `json:"cmdline,omitempty"`
}

// CheckContext provides dependencies, configuration, and seams to diagnostic checks.
type CheckContext struct {
	Home          string
	Root          string
	AgentHome     string
	Config        *config.Config
	Fix           bool
	Verbose       bool
	Now           func() time.Time
	HTTPClient    *http.Client
	CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)
	ProcessLister func() ([]ProcessInfo, error)
	PidAlive      func(pid int) bool
	GetEnv        func(key string) string
}

// Check is the interface implemented by all diagnostic checks.
type Check interface {
	ID() string
	Tier() Tier
	Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis
	Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error)
}

// Summary tallies the check results.
type Summary struct {
	Total int `json:"total"`
	Pass  int `json:"pass"`
	Warn  int `json:"warn"`
	Fail  int `json:"fail"`
	Fixed int `json:"fixed"`
}

// Result is the complete diagnostic assessment returned by the engine.
type Result struct {
	Timestamp time.Time     `json:"timestamp"`
	Healthy   bool          `json:"healthy"`
	Summary   Summary       `json:"summary"`
	Checks    []Diagnosis   `json:"checks"`
	Duration  time.Duration `json:"duration"`
}
