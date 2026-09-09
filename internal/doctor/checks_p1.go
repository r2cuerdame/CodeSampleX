package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/launcher"
	"github.com/r2cuerdame/codesamplex/internal/update"
)

// -----------------------------------------------------------------------------
// P1: Orphaned MCP Processes
// -----------------------------------------------------------------------------

type OrphanedMCPCheck struct{}

func (c *OrphanedMCPCheck) ID() string { return "p1.process.orphaned_mcp" }
func (c *OrphanedMCPCheck) Tier() Tier  { return TierP1 }

func (c *OrphanedMCPCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	lister := cctx.ProcessLister
	if lister == nil {
		lister = defaultProcessLister
	}

	procs, err := lister()
	if err != nil || len(procs) == 0 {
		d.Summary = "no processes inspected or process listing unavailable"
		return d
	}

	pidAlive := cctx.PidAlive
	if pidAlive == nil {
		pidAlive = update.LockPidAlive
	}

	myPid := os.Getpid()
	var orphans []ProcessInfo

	for _, p := range procs {
		if p.Pid == myPid {
			continue
		}
		lowerName := strings.ToLower(p.Name)
		isCSX := lowerName == "csx.exe" || lowerName == "csx" ||
			lowerName == "csx-payload.exe" || lowerName == "csx-payload" ||
			strings.Contains(lowerName, "csx")

		if !isCSX {
			continue
		}

		// Parent PID dead or missing
		if p.ParentPid <= 0 || !pidAlive(p.ParentPid) {
			orphans = append(orphans, p)
		}
	}

	if len(orphans) > 0 {
		d.Status = StatusWarn
		d.Fixable = true
		var pids []string
		for _, o := range orphans {
			pids = append(pids, fmt.Sprintf("%d (%s, parent %d)", o.Pid, o.Name, o.ParentPid))
		}
		d.Summary = fmt.Sprintf("found %d orphaned csx process(es) with dead parent PID", len(orphans))
		d.Details = pids
		d.Remediation = "Run 'csx doctor --fix' to terminate orphaned processes"
		return d
	}

	d.Summary = "no orphaned csx MCP processes detected"
	return d
}

func (c *OrphanedMCPCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	lister := cctx.ProcessLister
	if lister == nil {
		lister = defaultProcessLister
	}
	procs, err := lister()
	if err != nil {
		return diag, err
	}

	pidAlive := cctx.PidAlive
	if pidAlive == nil {
		pidAlive = update.LockPidAlive
	}
	myPid := os.Getpid()

	for _, p := range procs {
		if p.Pid == myPid {
			continue
		}
		lowerName := strings.ToLower(p.Name)
		if lowerName == "csx.exe" || lowerName == "csx" ||
			lowerName == "csx-payload.exe" || lowerName == "csx-payload" ||
			strings.Contains(lowerName, "csx") {
			if p.ParentPid <= 0 || !pidAlive(p.ParentPid) {
				if proc, perr := os.FindProcess(p.Pid); perr == nil {
					_ = proc.Kill()
				}
			}
		}
	}

	return c.Diagnose(ctx, cctx), nil
}

// -----------------------------------------------------------------------------
// P1: Stale Locks
// -----------------------------------------------------------------------------

type StaleLocksCheck struct{}

func (c *StaleLocksCheck) ID() string { return "p1.storage.stale_locks" }
func (c *StaleLocksCheck) Tier() Tier  { return TierP1 }

func (c *StaleLocksCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	pidAlive := cctx.PidAlive
	if pidAlive == nil {
		pidAlive = update.LockPidAlive
	}

	var staleFiles []string

	// 1. daemon.lock
	daemonLockPath := filepath.Join(cctx.Home, "daemon.lock")
	if raw, err := os.ReadFile(daemonLockPath); err == nil {
		pidStr := strings.TrimSpace(string(raw))
		pid, perr := strconv.Atoi(pidStr)
		if perr != nil || !pidAlive(pid) {
			staleFiles = append(staleFiles, fmt.Sprintf("daemon.lock (PID %d is dead)", pid))
			d.Fixable = true
		} else {
			d.Details = append(d.Details, fmt.Sprintf("daemon.lock held by active process (PID %d)", pid))
		}
	}

	// 2. .update.lock
	updateLockPath := filepath.Join(cctx.Root, ".update.lock")
	if raw, err := os.ReadFile(updateLockPath); err == nil {
		fields := strings.Fields(string(raw))
		isStale := false
		var pid int
		if len(fields) >= 2 {
			p, err := strconv.Atoi(fields[1])
			if err == nil {
				pid = p
				if !pidAlive(pid) {
					isStale = true
				}
			} else {
				isStale = true
			}
		} else {
			// Malformed lock
			fi, _ := os.Stat(updateLockPath)
			if fi != nil && time.Since(fi.ModTime()) > 24*time.Hour {
				isStale = true
			}
		}

		if isStale {
			staleFiles = append(staleFiles, fmt.Sprintf(".update.lock (PID %d is dead)", pid))
			d.Fixable = true
		} else if pid > 0 {
			d.Details = append(d.Details, fmt.Sprintf(".update.lock held by active process (PID %d)", pid))
		}
	}

	if len(staleFiles) > 0 {
		d.Status = StatusWarn
		d.Summary = fmt.Sprintf("found %d stale lock file(s) with dead owner PID", len(staleFiles))
		d.Details = append(d.Details, staleFiles...)
		d.Remediation = "Run 'csx doctor --fix' to safely remove stale locks"
		return d
	}

	d.Summary = "no stale lock files found"
	return d
}

func (c *StaleLocksCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	pidAlive := cctx.PidAlive
	if pidAlive == nil {
		pidAlive = update.LockPidAlive
	}

	// Remove daemon.lock ONLY if PID is dead
	daemonLockPath := filepath.Join(cctx.Home, "daemon.lock")
	if raw, err := os.ReadFile(daemonLockPath); err == nil {
		pidStr := strings.TrimSpace(string(raw))
		pid, perr := strconv.Atoi(pidStr)
		if perr != nil || !pidAlive(pid) {
			_ = os.Remove(daemonLockPath)
		}
	}

	// Remove .update.lock ONLY if PID is dead
	updateLockPath := filepath.Join(cctx.Root, ".update.lock")
	if raw, err := os.ReadFile(updateLockPath); err == nil {
		fields := strings.Fields(string(raw))
		isStale := false
		if len(fields) >= 2 {
			if pid, err := strconv.Atoi(fields[1]); err == nil {
				if !pidAlive(pid) {
					isStale = true
				}
			} else {
				isStale = true
			}
		} else {
			fi, _ := os.Stat(updateLockPath)
			if fi != nil && time.Since(fi.ModTime()) > 24*time.Hour {
				isStale = true
			}
		}
		if isStale {
			_ = os.Remove(updateLockPath)
		}
	}

	return c.Diagnose(ctx, cctx), nil
}

// -----------------------------------------------------------------------------
// P1: Stale Payloads & Displaced Binaries
// -----------------------------------------------------------------------------

type StalePayloadsCheck struct{}

func (c *StalePayloadsCheck) ID() string { return "p1.storage.stale_payloads" }
func (c *StalePayloadsCheck) Tier() Tier  { return TierP1 }

func (c *StalePayloadsCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	act, err := launcher.Read(cctx.Root)
	if err != nil {
		if raw, rerr := os.ReadFile(launcher.Path(cctx.Root)); rerr == nil {
			_ = json.Unmarshal(raw, &act)
		}
	}
	referenced := make(map[string]bool)
	if act.Current.Version != "" {
		referenced[act.Current.Version] = true
	}
	if act.Previous != nil && act.Previous.Version != "" {
		referenced[act.Previous.Version] = true
	}
	if act.RollbackHold != nil && act.RollbackHold.Version != "" {
		referenced[act.RollbackHold.Version] = true
	}

	var stalePayloadDirs []string
	if len(referenced) > 0 {
		payloadsDir := filepath.Join(cctx.Root, "payloads")
		if entries, err := os.ReadDir(payloadsDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					v := e.Name()
					if !referenced[v] {
						stalePayloadDirs = append(stalePayloadDirs, filepath.Join(payloadsDir, v))
					}
				}
			}
		}
	}

	var displacedBinaries []string
	if rootEntries, err := os.ReadDir(cctx.Root); err == nil {
		for _, e := range rootEntries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, "csx.exe.old-") || strings.HasPrefix(name, "csx.exe.previous-") ||
				strings.HasPrefix(name, "csx.old-") || strings.HasPrefix(name, "csx.previous-") {
				displacedBinaries = append(displacedBinaries, filepath.Join(cctx.Root, name))
			}
		}
	}

	if len(stalePayloadDirs) > 0 || len(displacedBinaries) > 0 {
		d.Status = StatusWarn
		d.Fixable = true
		d.Summary = fmt.Sprintf("found %d unreferenced payload directory(ies) and %d displaced binary aside(s)",
			len(stalePayloadDirs), len(displacedBinaries))
		for _, p := range stalePayloadDirs {
			d.Details = append(d.Details, "stale payload: "+p)
		}
		for _, b := range displacedBinaries {
			d.Details = append(d.Details, "displaced binary: "+b)
		}
		d.Remediation = "Run 'csx doctor --fix' to remove unreferenced payload versions and displaced binaries"
		return d
	}

	d.Summary = "no unreferenced payloads or displaced binaries found"
	return d
}

func (c *StalePayloadsCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	act, err := launcher.Read(cctx.Root)
	if err != nil {
		if raw, rerr := os.ReadFile(launcher.Path(cctx.Root)); rerr == nil {
			_ = json.Unmarshal(raw, &act)
		}
	}
	referenced := make(map[string]bool)
	if act.Current.Version != "" {
		referenced[act.Current.Version] = true
	}
	if act.Previous != nil && act.Previous.Version != "" {
		referenced[act.Previous.Version] = true
	}
	if act.RollbackHold != nil && act.RollbackHold.Version != "" {
		referenced[act.RollbackHold.Version] = true
	}

	if len(referenced) > 0 {
		payloadsDir := filepath.Join(cctx.Root, "payloads")
		if entries, err := os.ReadDir(payloadsDir); err == nil {
			for _, e := range entries {
				if e.IsDir() && !referenced[e.Name()] {
					_ = os.RemoveAll(filepath.Join(payloadsDir, e.Name()))
				}
			}
		}
	}

	if rootEntries, err := os.ReadDir(cctx.Root); err == nil {
		for _, e := range rootEntries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, "csx.exe.old-") || strings.HasPrefix(name, "csx.exe.previous-") ||
				strings.HasPrefix(name, "csx.old-") || strings.HasPrefix(name, "csx.previous-") {
				_ = os.Remove(filepath.Join(cctx.Root, name))
			}
		}
	}

	return c.Diagnose(ctx, cctx), nil
}

// -----------------------------------------------------------------------------
// P1: MCP Agent Config
// -----------------------------------------------------------------------------

type MCPAgentConfigCheck struct{}

func (c *MCPAgentConfigCheck) ID() string { return "p1.mcp.agent_config" }
func (c *MCPAgentConfigCheck) Tier() Tier  { return TierP1 }

func (c *MCPAgentConfigCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	agentHome := cctx.AgentHome
	if agentHome == "" {
		if h, err := os.UserHomeDir(); err == nil {
			agentHome = h
		}
	}

	type agentTarget struct {
		name       string
		configFile string
		extractCmd func(content []byte) string
	}

	targets := []agentTarget{
		{
			name:       "Claude Code",
			configFile: filepath.Join(agentHome, ".claude.json"),
			extractCmd: func(content []byte) string {
				var m map[string]any
				if err := json.Unmarshal(content, &m); err != nil {
					return ""
				}
				servers, _ := m["mcpServers"].(map[string]any)
				csx, _ := servers["csx"].(map[string]any)
				cmd, _ := csx["command"].(string)
				return cmd
			},
		},
		{
			name:       "Gemini CLI",
			configFile: filepath.Join(agentHome, ".gemini", "settings.json"),
			extractCmd: func(content []byte) string {
				var m map[string]any
				if err := json.Unmarshal(content, &m); err != nil {
					return ""
				}
				servers, _ := m["mcpServers"].(map[string]any)
				csx, _ := servers["csx"].(map[string]any)
				cmd, _ := csx["command"].(string)
				return cmd
			},
		},
		{
			name:       "OpenCode",
			configFile: filepath.Join(agentHome, ".config", "opencode", "opencode.json"),
			extractCmd: func(content []byte) string {
				var m map[string]any
				if err := json.Unmarshal(content, &m); err != nil {
					return ""
				}
				mcp, _ := m["mcp"].(map[string]any)
				csx, _ := mcp["csx"].(map[string]any)
				cmd, _ := csx["command"].(string)
				return cmd
			},
		},
	}

	var brokenAgents []string
	checkedCount := 0

	for _, tgt := range targets {
		if raw, err := os.ReadFile(tgt.configFile); err == nil {
			checkedCount++
			cmd := tgt.extractCmd(raw)
			if cmd != "" && (filepath.IsAbs(cmd) || strings.ContainsAny(cmd, `/\`)) {
				if !fileExists(cmd) {
					brokenAgents = append(brokenAgents, fmt.Sprintf("%s (%s points to missing %s)", tgt.name, tgt.configFile, cmd))
					d.Fixable = true
				} else {
					d.Details = append(d.Details, fmt.Sprintf("%s: verified MCP binary at %s", tgt.name, cmd))
				}
			}
		}
	}

	if len(brokenAgents) > 0 {
		d.Status = StatusWarn
		d.Summary = fmt.Sprintf("found %d agent configuration(s) pointing to missing binary", len(brokenAgents))
		d.Details = append(d.Details, brokenAgents...)
		d.Remediation = "Run 'csx doctor --fix' to update agent configurations with the current valid binary path"
		return d
	}

	if checkedCount > 0 {
		d.Summary = fmt.Sprintf("checked %d agent configuration(s); all registered MCP binary paths exist", checkedCount)
	} else {
		d.Summary = "no local agent configurations detected"
	}
	return d
}

func (c *MCPAgentConfigCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	agentHome := cctx.AgentHome
	if agentHome == "" {
		if h, err := os.UserHomeDir(); err == nil {
			agentHome = h
		}
	}

	exeName := "csx"
	if runtime.GOOS == "windows" {
		exeName = "csx.exe"
	}
	validExe := filepath.Join(cctx.Root, exeName)
	if !fileExists(validExe) {
		if exe, err := os.Executable(); err == nil {
			validExe = exe
		}
	}

	// Update Claude Code
	claudePath := filepath.Join(agentHome, ".claude.json")
	if raw, err := os.ReadFile(claudePath); err == nil {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			servers, _ := m["mcpServers"].(map[string]any)
			if servers == nil {
				servers = make(map[string]any)
			}
			servers["csx"] = map[string]any{"command": validExe, "args": []any{"mcp"}}
			m["mcpServers"] = servers
			if out, err := json.MarshalIndent(m, "", "  "); err == nil {
				_ = os.WriteFile(claudePath, append(out, '\n'), 0o600)
			}
		}
	}

	// Update Gemini CLI
	geminiPath := filepath.Join(agentHome, ".gemini", "settings.json")
	if raw, err := os.ReadFile(geminiPath); err == nil {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			servers, _ := m["mcpServers"].(map[string]any)
			if servers == nil {
				servers = make(map[string]any)
			}
			servers["csx"] = map[string]any{"command": validExe, "args": []any{"mcp"}}
			m["mcpServers"] = servers
			if out, err := json.MarshalIndent(m, "", "  "); err == nil {
				_ = os.WriteFile(geminiPath, append(out, '\n'), 0o600)
			}
		}
	}

	// Update OpenCode
	openCodePath := filepath.Join(agentHome, ".config", "opencode", "opencode.json")
	if raw, err := os.ReadFile(openCodePath); err == nil {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			mcpMap, _ := m["mcp"].(map[string]any)
			if mcpMap == nil {
				mcpMap = make(map[string]any)
			}
			mcpMap["csx"] = map[string]any{"command": validExe, "args": []any{"mcp"}}
			m["mcp"] = mcpMap
			if out, err := json.MarshalIndent(m, "", "  "); err == nil {
				_ = os.WriteFile(openCodePath, append(out, '\n'), 0o600)
			}
		}
	}

	return c.Diagnose(ctx, cctx), nil
}

// -----------------------------------------------------------------------------
// P1: Auth Session Validity (ZERO SECRET LEAKAGE)
// -----------------------------------------------------------------------------

type AuthSessionValidityCheck struct{}

func (c *AuthSessionValidityCheck) ID() string { return "p1.auth.session_validity" }
func (c *AuthSessionValidityCheck) Tier() Tier  { return TierP1 }

func (c *AuthSessionValidityCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	if cctx.Config == nil || (cctx.Config.APIToken == "" && cctx.Config.GithubLogin == "") {
		d.Summary = "session unauthenticated (anonymous mode)"
		return d
	}

	tok := cctx.Config.APIToken
	login := cctx.Config.GithubLogin

	if tok != "" {
		// Verify format: prefix csx_ and valid token structure
		if !strings.HasPrefix(tok, "csx_") || len(tok) < 16 {
			d.Status = StatusWarn
			d.Fixable = true
			d.Summary = "configured API token has invalid format"
			d.Details = append(d.Details, "token does not match expected 'csx_' format")
			d.Remediation = "Run 'csx login' to re-authenticate or 'csx doctor --fix' to clear invalid credentials"
			return d
		}

		// Masked detail: NEVER emit raw token
		masked := "csx_***"
		d.Summary = fmt.Sprintf("authenticated session valid for %s (%s, length %d)", login, masked, len(tok))
		d.Details = append(d.Details, fmt.Sprintf("githubLogin: %s", login))
		d.Details = append(d.Details, fmt.Sprintf("token: %s (length %d)", masked, len(tok)))
		return d
	}

	d.Summary = fmt.Sprintf("session configured for user %s (no API token)", login)
	return d
}

func (c *AuthSessionValidityCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	if cctx.Config != nil && (cctx.Config.APIToken != "" && !strings.HasPrefix(cctx.Config.APIToken, "csx_")) {
		cctx.Config.APIToken = ""
		_ = cctx.Config.Save(cctx.Home)
	}
	return c.Diagnose(ctx, cctx), nil
}

// -----------------------------------------------------------------------------
// P1: Network Registries Reachability
// -----------------------------------------------------------------------------

type NetworkRegistriesCheck struct{}

func (c *NetworkRegistriesCheck) ID() string { return "p1.network.registries" }
func (c *NetworkRegistriesCheck) Tier() Tier  { return TierP1 }

func (c *NetworkRegistriesCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	if cctx.Config != nil && cctx.Config.Mode == config.ModeLocalOnly {
		d.Summary = "local-only mode: registry contact disabled by privacy policy"
		return d
	}

	client := cctx.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	targetURL := update.DefaultManifestURL
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, targetURL, nil)
	if err != nil {
		d.Status = StatusWarn
		d.Summary = fmt.Sprintf("invalid release URL: %v", err)
		return d
	}

	resp, err := client.Do(req)
	if err != nil {
		// Fallback to GET with small range or limit
		reqGet, gerr := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if gerr == nil {
			reqGet.Header.Set("Range", "bytes=0-0")
			respGet, gerr2 := client.Do(reqGet)
			if gerr2 == nil {
				respGet.Body.Close()
				d.Summary = "GitHub release assets reachable"
				return d
			}
		}
		d.Status = StatusWarn
		d.Summary = fmt.Sprintf("GitHub release registry unreachable: %v", err)
		d.Remediation = "Verify network connection, proxy settings, or GitHub access"
		d.Error = err.Error()
		return d
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		d.Summary = fmt.Sprintf("GitHub release registry reachable (HTTP %d)", resp.StatusCode)
		return d
	}

	d.Status = StatusWarn
	d.Summary = fmt.Sprintf("GitHub release registry returned HTTP %d", resp.StatusCode)
	return d
}

func (c *NetworkRegistriesCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	return diag, errors.New("network reachability is an external condition and cannot be locally repaired")
}
