package doctor

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/update"
)

// DefaultLauncherRoot resolves the standard installation root for csx.
func DefaultLauncherRoot() string {
	if r := os.Getenv("CSX_LAUNCHER_ROOT"); r != "" {
		return r
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "active.json")); err == nil {
			return dir
		}
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		return filepath.Join(local, "csx")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "share", "csx")
	}
	return ""
}

// DefaultCheckContext returns a CheckContext initialized with real environment dependencies.
func DefaultCheckContext(fix, verbose bool) (*CheckContext, error) {
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(home)
	if err != nil {
		cfg = config.Default()
	}

	root := DefaultLauncherRoot()

	agentHome := os.Getenv("CSX_AGENT_HOME")
	if strings.TrimSpace(agentHome) == "" {
		if h, err := os.UserHomeDir(); err == nil {
			agentHome = h
		}
	}

	return &CheckContext{
		Home:       home,
		Root:       root,
		AgentHome:  agentHome,
		Config:     cfg,
		Fix:        fix,
		Verbose:    verbose,
		Now:        time.Now,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		CommandRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			return exec.CommandContext(tctx, name, args...).CombinedOutput()
		},
		ProcessLister: defaultProcessLister,
		PidAlive:      update.LockPidAlive,
		GetEnv:        os.Getenv,
	}, nil
}

// Engine coordinates the execution of diagnostic checks.
type Engine struct {
	checks []Check
}

// NewEngine constructs an Engine with explicit checks.
func NewEngine(checks ...Check) *Engine {
	return &Engine{checks: checks}
}

// DefaultEngine constructs an Engine with all canonical P0 and P1 checks registered.
func DefaultEngine() *Engine {
	return NewEngine(
		// P0 Checks
		&LauncherIntegrityCheck{},
		&PayloadVerificationCheck{},
		&LauncherConsistencyCheck{},
		&ManifestReleaseBindingCheck{},
		&MCPViabilityCheck{},
		&ServerCompatibilityCheck{},
		&StorageLocalDBCheck{},

		// P1 Checks
		&OrphanedMCPCheck{},
		&StaleLocksCheck{},
		&StalePayloadsCheck{},
		&MCPAgentConfigCheck{},
		&AuthSessionValidityCheck{},
		&NetworkRegistriesCheck{},
	)
}

// Run executes all checks, optionally applies safe self-healing repairs when requested,
// re-verifies outcomes, and returns a fully redacted Result.
func (e *Engine) Run(ctx context.Context, cctx *CheckContext) Result {
	start := time.Now()
	var diagnoses []Diagnosis

	for _, ch := range e.checks {
		diag := ch.Diagnose(ctx, cctx)

		// Self-healing attempt if --fix is active, check is fixable and currently not passing
		if cctx.Fix && diag.Fixable && (diag.Status == StatusFail || diag.Status == StatusWarn) {
			repairedDiag, err := ch.Repair(ctx, cctx, diag)
			if err == nil {
				// Re-verify outcome after repair
				reverified := ch.Diagnose(ctx, cctx)
				if reverified.Status == StatusPass {
					diag.Status = StatusFixed
					diag.Summary = "repaired successfully: " + reverified.Summary
					diag.Details = append(diag.Details, "self-healing repair applied and re-verified")
				} else {
					diag = reverified
				}
			} else {
				diag.Error = err.Error()
				diag.Details = append(diag.Details, "repair attempted but failed: "+err.Error())
			}
			_ = repairedDiag
		}

		diagnoses = append(diagnoses, diag)
	}

	summary := Summary{
		Total: len(diagnoses),
	}
	for _, d := range diagnoses {
		switch d.Status {
		case StatusPass:
			summary.Pass++
		case StatusWarn:
			summary.Warn++
		case StatusFail:
			summary.Fail++
		case StatusFixed:
			summary.Fixed++
		}
	}

	res := Result{
		Timestamp: start.UTC(),
		Healthy:   summary.Fail == 0,
		Summary:   summary,
		Checks:    diagnoses,
		Duration:  time.Since(start),
	}

	var explicitTokens []string
	if cctx.Config != nil && cctx.Config.APIToken != "" {
		explicitTokens = append(explicitTokens, cctx.Config.APIToken)
	}

	return SanitizeResult(res, explicitTokens...)
}
