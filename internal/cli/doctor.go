package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/launcher"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

// The output is intentionally an allowlist of codes and messages. Neither an
// HTTP error, config parse error, environment variable, nor filesystem path is
// copied into a report: all of them may contain credentials.
type doctorCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Action string `json:"action,omitempty"`
}

type doctorReport struct {
	SchemaVersion int           `json:"schemaVersion"`
	Health        string        `json:"health"`
	Checks        []doctorCheck `json:"checks"`
}

var (
	doctorOutput               io.Writer = os.Stdout
	doctorHome                           = config.Home
	doctorExecutable                     = os.Executable
	doctorHTTP                           = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	doctorRehydrate                      = csxupdate.RehydrateInstall
	doctorRepairReleaseBinding           = csxupdate.RepairReleaseBinding
	doctorMCPProbe                       = probeMCPStartup
	doctorMCPProcesses                   = inspectMCPProcesses
	doctorLauncherVersionProbe           = probeLauncherVersion
)

func init() {
	Register(Command{Name: "doctor", Summary: "diagnose and safely repair this csx installation", Run: doctorMain})
}

func doctorMain(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fix := fs.Bool("fix", false, "repair CSX-owned state")
	verbose := fs.Bool("verbose", false, "show check details")
	asJSON := fs.Bool("json", false, "machine-readable report")
	if err := fs.Parse(args); err != nil || len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: csx doctor [--fix] [--verbose] [--json]")
		return 2
	}
	home, homeErr := doctorHome()
	exe, exeErr := doctorExecutable()
	budget := 25 * time.Second
	if *fix {
		budget = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	before := diagnose(ctx, home, homeErr, exe, exeErr)
	result := before
	if *fix {
		attemptRepairs(ctx, home, exe, before)
		result = diagnose(ctx, home, homeErr, exe, exeErr)
		for i := range result.Checks {
			if result.Checks[i].Status != "PASS" {
				continue
			}
			for _, old := range before.Checks {
				if old.ID == result.Checks[i].ID && (old.Status == "FAIL" || (old.ID == "cache-stale" && old.Status == "WARN")) {
					result.Checks[i].Status = "FIXED"
				}
			}
		}
	}
	if *asJSON {
		_ = json.NewEncoder(doctorOutput).Encode(result)
	} else {
		for _, check := range result.Checks {
			fmt.Fprintf(doctorOutput, "%s %-22s %s\n", check.Status, check.ID, check.Detail)
			if *verbose {
				fmt.Fprintf(doctorOutput, "  code: %s\n", check.Code)
			}
			if (*verbose || check.Status == "FAIL") && check.Action != "" {
				fmt.Fprintf(doctorOutput, "  action: %s\n", check.Action)
			}
		}
		fmt.Fprintf(doctorOutput, "%s\n", result.Health)
	}
	if result.Health == "UNHEALTHY" {
		return 1
	}
	return 0
}

func diagnose(ctx context.Context, home string, homeErr error, exe string, exeErr error) doctorReport {
	r := doctorReport{SchemaVersion: 1, Health: "HEALTHY", Checks: []doctorCheck{}}
	add := func(id, status, code, detail, action string) {
		r.Checks = append(r.Checks, doctorCheck{id, status, code, detail, action})
		if status == "FAIL" {
			r.Health = "UNHEALTHY"
		}
	}
	if homeErr != nil || home == "" {
		add("home", "FAIL", "home-unavailable", "CSX home cannot be resolved", "Set a valid CSX_HOME and rerun doctor")
		return r
	}
	if !filepath.IsAbs(home) {
		add("home", "FAIL", "home-not-absolute", "CSX home is not an absolute path", "Set CSX_HOME to an absolute CSX-owned directory")
		return r
	}
	if fi, err := os.Lstat(home); err == nil && (!fi.IsDir() || fi.Mode()&os.ModeSymlink != 0) {
		add("home", "FAIL", "home-invalid", "CSX home is not a regular directory", "Inspect CSX_HOME manually")
		return r
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		add("home", "FAIL", "home-unreadable", "CSX home cannot be inspected", "Inspect CSX_HOME manually")
		return r
	}
	if exeErr != nil || exe == "" {
		add("executable", "FAIL", "executable-unavailable", "Running executable cannot be resolved", "Reinstall CSX from the official installer")
		return r
	}
	info, err := os.Lstat(exe)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		add("executable", "FAIL", "executable-invalid", "Running executable is missing or not a regular file", "Reinstall CSX from the official installer")
	} else {
		add("executable", "PASS", "executable-regular", "Running executable is a regular file", "")
	}
	if !csxupdate.IsCanonicalReleaseVersion(Version) {
		add("release", "WARN", "development-build", "Build has no stable release identity", "Use an official release to verify signed release binding")
	} else {
		add("release", "PASS", "release-version", "Client has a canonical release version", "")
	}
	install, installErr := csxupdate.LoadInstall(home)
	if installErr != nil {
		add("install", "WARN", "ownership-unavailable", "CSX install marker is unavailable", "Run the official installer to register this installation")
	} else if install.Kind == "launcher" {
		if install.InstallRoot == "" || install.LauncherPath == "" {
			add("install", "FAIL", "launcher-path-invalid", "Launcher marker lacks a path", "Run the official installer")
		} else if a, err := launcher.Read(install.InstallRoot); err != nil {
			add("install", "FAIL", "pointer-invalid", "Launcher pointer cannot be authenticated", "Run the official installer; do not edit active.json")
		} else {
			add("install", "PASS", "launcher-pointer", "Launcher pointer is readable", "")
			if err := launcher.VerifyPayload(install.InstallRoot, a.Current); err != nil {
				add("payload", "FAIL", launcher.Reason(err), "Active payload fails its recorded SHA-256", "Run csx doctor --fix")
			} else if a.Current.Version != Version {
				add("payload", "FAIL", "launcher-payload-version", "Running payload and active pointer name different releases", "Run csx doctor --fix; if this persists run the official installer")
			} else {
				add("payload", "PASS", "payload-verified", "Active payload matches its recorded SHA-256 and version", "")
			}
			if runtime.GOOS == "windows" {
				checkLauncher(install, a, add)
			}
			checkStalePayloads(install.InstallRoot, a, add)
		}
	} else if install.ExecutablePath != "" && !sameDoctorPath(install.ExecutablePath, exe) {
		add("install", "FAIL", "standalone-path-stale", "Install marker points to a different executable", "Run the official installer")
	} else {
		add("install", "PASS", "standalone-marker", "Standalone install marker matches this executable", "")
	}
	if st, err := csxupdate.LoadState(home); err != nil {
		add("update-state", "FAIL", "update-state-invalid", "Update state cannot be read", "Preserve the state file and reinstall through the official installer")
	} else if installErr == nil && install.Kind == "launcher" && st.HighestVersion != "" && csxupdate.IsCanonicalReleaseVersion(st.HighestVersion) {
		add("update-state", "PASS", "update-floor-readable", "Trusted release floor is readable", "")
	} else {
		add("update-state", "PASS", "update-state-readable", "Update state is readable", "")
	}
	if csxupdate.UpdateLockStale(home) {
		add("update-lock", "FAIL", "stale-update-lock", "CSX updater lock has no live owner", "Run csx doctor --fix")
	} else {
		add("update-lock", "PASS", "update-lock-ready", "No stale CSX updater lock found", "")
	}
	cfg, cfgErr := config.Load(home)
	if cfgErr != nil {
		add("config", "FAIL", "config-invalid", "CSX configuration cannot be parsed", "Repair config.json manually without deleting credentials")
	} else {
		add("config", "PASS", "config-readable", "CSX configuration is readable", "")
		if cfg.GithubLogin != "" && cfg.APIToken == "" {
			add("auth", "FAIL", "session-incomplete", "Login has no API token", "Run csx login github")
		} else if cfg.APIToken != "" && !validDoctorToken(cfg.APIToken) {
			add("auth", "FAIL", "token-format-invalid", "Saved API token format is invalid", "Run csx login github")
		} else if cfg.APIToken != "" {
			add("auth", "WARN", "session-not-verifiable", "Token format is valid; server has no read-only session endpoint", "Retry login if authenticated operations fail")
		} else {
			add("auth", "PASS", "anonymous-session", "No authentication session is configured", "")
		}
	}
	for _, dir := range []string{"cas", "samples", "logs"} {
		path := filepath.Join(home, dir)
		if fi, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			add("cache-"+dir, "FAIL", "cache-directory-missing", "CSX state directory is missing", "Run csx doctor --fix")
		} else if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			add("cache-"+dir, "FAIL", "cache-directory-invalid", "CSX state directory is not a regular directory", "Inspect this path manually; doctor will not replace it")
		} else {
			add("cache-"+dir, "PASS", "cache-directory-ready", "CSX state directory exists", "")
		}
	}
	checkStaleCache(home, add)
	dbPath := filepath.Join(home, "csx.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		add("local-db", "WARN", "database-not-created", "Local database has not been initialized", "")
	} else if err != nil {
		add("local-db", "FAIL", "database-unreadable", "Local database cannot be inspected", "Inspect csx.db manually; preserve queued evidence")
	} else if db, err := localdb.OpenReadOnly(ctx, dbPath); err != nil {
		add("local-db", "FAIL", "database-unreadable", "Local database cannot be inspected", "Inspect csx.db manually; preserve queued evidence")
	} else {
		current, checkErr := db.DoctorCheck(ctx)
		_ = db.Close()
		if checkErr != nil {
			add("local-db", "FAIL", "database-integrity", "Local database integrity check failed", "Preserve csx.db and repair manually")
		} else if !current {
			add("local-db", "FAIL", "database-schema-stale", "Local database schema is stale", "Run csx doctor --fix")
		} else {
			add("local-db", "PASS", "database-ready", "Local database integrity and schema are current", "")
		}
	}
	cmd := mcpCommand()
	if fi, err := os.Stat(cmd); err != nil || !fi.Mode().IsRegular() {
		add("mcp", "FAIL", "mcp-command-missing", "MCP executable is unavailable", "Run the official installer or update the CSX MCP registration")
	} else if err := doctorMCPProbe(ctx, cmd); err != nil {
		add("mcp", "FAIL", "mcp-startup-failed", "MCP initialize handshake failed", "Run csx doctor --fix, then restart the MCP host")
	} else {
		add("mcp", "PASS", "mcp-startup-ready", "MCP executable completed an isolated initialize handshake", "")
	}
	if stale, inspected := doctorMCPProcesses(ctx, cmd); !inspected {
		add("mcp-processes", "WARN", "process-inspection-unavailable", "MCP process list cannot be inspected", "Restart the MCP host if an old session persists")
	} else if stale {
		add("mcp-processes", "WARN", "stale-mcp-process", "An old or orphaned CSX MCP process is running", "Restart its MCP host to replace the session")
	} else {
		add("mcp-processes", "PASS", "mcp-processes-current", "No stale CSX MCP process found", "")
	}
	if cfgErr == nil {
		checkServer(ctx, cfg, add)
		checkRegistries(ctx, cfg, add)
	}
	checkCodexRegistration(add)
	checkManifest(ctx, install, installErr, add)
	return r
}

func validDoctorToken(token string) bool {
	if len(token) != 52 || !strings.HasPrefix(token, "csx_") {
		return false
	}
	_, err := hex.DecodeString(token[4:])
	return err == nil
}

func checkStalePayloads(root string, a launcher.Active, add func(string, string, string, string, string)) {
	entries, err := os.ReadDir(filepath.Join(root, "payloads"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		add("payload-entries", "WARN", "payload-entries-unreadable", "Payload directory cannot be inspected", "Inspect CSX payloads manually")
		return
	}
	kept := map[string]bool{a.Current.Version: true}
	if a.Previous != nil {
		kept[a.Previous.Version] = true
	}
	if a.RollbackHold != nil {
		kept[a.RollbackHold.Version] = true
	}
	stale := false
	for _, entry := range entries {
		if entry.IsDir() && csxupdate.IsCanonicalReleaseVersion(entry.Name()) && !kept[entry.Name()] {
			stale = true
			break
		}
	}
	if stale {
		add("payload-entries", "WARN", "stale-payload-entry", "Unreferenced CSX payload directories remain", "Review release history before removing old payloads manually")
	} else {
		add("payload-entries", "PASS", "payload-entries-current", "No unreferenced CSX payload directories found", "")
	}
}

func staleCacheTemps(home string) []string {
	root := filepath.Join(home, "cas")
	fi, err := os.Lstat(root)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var found []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "tmp-") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		fi, err := os.Lstat(path)
		if err == nil && fi.Mode().IsRegular() && time.Since(fi.ModTime()) > 24*time.Hour {
			found = append(found, path)
		}
	}
	return found
}

func checkStaleCache(home string, add func(string, string, string, string, string)) {
	if len(staleCacheTemps(home)) > 0 {
		add("cache-stale", "WARN", "stale-cache-temp", "Old uncommitted CSX cache staging files remain", "Run csx doctor --fix")
	} else {
		add("cache-stale", "PASS", "cache-temp-clean", "No stale CSX cache staging files found", "")
	}
}

func codexDoctorPath() string {
	home, _, err := resolveAgentHome(os.UserHomeDir)
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
}

func checkCodexRegistration(add func(string, string, string, string, string)) {
	path := codexDoctorPath()
	if path == "" {
		add("mcp-config", "WARN", "agent-home-unavailable", "Agent home cannot be inspected", "Check CSX_AGENT_HOME")
		return
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		add("mcp-config", "WARN", "codex-not-configured", "Codex MCP registration is not present", "Run csx init if using Codex")
		return
	}
	if err != nil {
		add("mcp-config", "FAIL", "codex-config-unreadable", "Codex configuration cannot be read", "Inspect the agent configuration manually")
		return
	}
	content := string(raw)
	i, j := strings.Index(content, tomlBegin), strings.Index(content, tomlEnd)
	if i < 0 && j < 0 {
		add("mcp-config", "WARN", "csx-block-absent", "Codex has no CSX-owned MCP block", "Run csx init if using Codex")
		return
	}
	if i < 0 || j <= i || strings.Count(content, tomlBegin) != 1 || strings.Count(content, tomlEnd) != 1 {
		add("mcp-config", "FAIL", "csx-block-invalid", "CSX-owned Codex MCP block is malformed", "Repair the marker pair manually before rerunning csx init")
		return
	}
	current := strings.TrimSpace(content[i+len(tomlBegin) : j])
	if current != strings.TrimSpace(codexMCPBlock()) {
		add("mcp-config", "FAIL", "csx-block-stale", "CSX-owned Codex MCP path or options are stale", "Run csx doctor --fix, then restart Codex")
		return
	}
	add("mcp-config", "PASS", "csx-block-current", "CSX-owned Codex MCP block matches this install", "")
}

func sameDoctorPath(a, b string) bool {
	a, _ = filepath.Abs(a)
	b, _ = filepath.Abs(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func checkLauncher(in csxupdate.Install, a launcher.Active, add func(string, string, string, string, string)) {
	fi, err := os.Lstat(in.LauncherPath)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		add("launcher", "FAIL", "launcher-invalid", "Launcher executable is missing or invalid", "Run the official installer")
		return
	}
	if a.Current.Version != Version {
		add("launcher", "FAIL", "launcher-payload-pair", "Launcher and running payload disagree on active release", "Run csx doctor --fix; if this persists run the official installer")
		return
	}
	add("launcher", "PASS", "launcher-ready", "Launcher executable and active release are present", "")
}

func checkServer(ctx context.Context, cfg *config.Config, add func(string, string, string, string, string)) {
	if cfg.Mode != config.ModeCommunity {
		add("server", "WARN", "network-disabled", "Server check skipped by local mode", "")
		return
	}
	u, err := url.Parse(cfg.ServerURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		add("server", "FAIL", "server-url-invalid", "Server URL is invalid", "Correct serverUrl in CSX config")
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.ServerURL, "/")+"/version", nil)
	resp, err := doctorHTTP.Do(req)
	if err != nil {
		add("server", "WARN", "server-unreachable", "Server is temporarily unreachable", "Check network and retry doctor")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		add("server", "WARN", "server-http-status", "Server version endpoint is unavailable", "Check server status and retry doctor")
		return
	}
	var identity struct {
		Service string `json:"service"`
		Version string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&identity) != nil || identity.Service != "csx-server" {
		add("server", "FAIL", "server-identity-invalid", "Endpoint is not a CSX server", "Correct serverUrl in CSX config")
		return
	}
	if identity.Version == "" {
		add("server", "WARN", "server-version-missing", "CSX server did not advertise a version", "Check the server deployment")
		return
	}
	add("server", "PASS", "server-version", "CSX server identity and version endpoint responded", "")
	apiReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.ServerURL, "/")+"/v1/adapters", nil)
	apiResp, apiErr := doctorHTTP.Do(apiReq)
	if apiErr != nil {
		add("server-api", "WARN", "server-api-unreachable", "CSX API route is temporarily unreachable", "Check server status and retry doctor")
		return
	}
	defer apiResp.Body.Close()
	if apiResp.StatusCode != http.StatusOK {
		add("server-api", "FAIL", "server-api-incompatible", "CSX server does not provide the required API route", "Update the CSX server or correct serverUrl")
		return
	}
	add("server-api", "PASS", "server-api-ready", "CSX server provides the required API route", "")
}

func checkRegistries(ctx context.Context, cfg *config.Config, add func(string, string, string, string, string)) {
	if cfg.Mode != config.ModeCommunity {
		add("registries", "WARN", "network-disabled", "Public registry check skipped by local mode", "")
		return
	}
	probes := []struct{ id, url string }{
		{"npm", "https://registry.npmjs.org/-/ping"},
		{"pypi", "https://pypi.org/"},
		{"crates", "https://index.crates.io/config.json"},
		{"go", "https://proxy.golang.org/github.com/google/uuid/@v/list"},
	}
	ready := make([]bool, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Add(1)
		go func(i int, p struct{ id, url string }) {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
			resp, err := doctorHTTP.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			ready[i] = resp.StatusCode >= 200 && resp.StatusCode < 400
		}(i, p)
	}
	wg.Wait()
	for i, p := range probes {
		if ready[i] {
			add("registry-"+p.id, "PASS", "registry-reachable", "Public registry is reachable", "")
		} else {
			add("registry-"+p.id, "WARN", "registry-unreachable", "Public registry is temporarily unavailable", "Check network and retry doctor")
		}
	}
}

func checkManifest(ctx context.Context, in csxupdate.Install, installErr error, add func(string, string, string, string, string)) {
	if !csxupdate.IsCanonicalReleaseVersion(Version) {
		add("stable-manifest", "WARN", "development-build", "Signed release check requires a release build", "")
		return
	}
	m, err := signedDoctorManifest(ctx, Version)
	if err != nil {
		if errors.Is(err, csxupdate.ErrDoctorReleaseUnavailable) {
			add("stable-manifest", "FAIL", "release-unreachable", "Signed stable release is temporarily unreachable", "Check network and retry doctor; never bypass signature verification")
		} else {
			add("stable-manifest", "FAIL", "manifest-unverified", "Signed stable release could not be verified", "Check official release; never bypass signature verification")
		}
		return
	}
	asset, err := csxupdate.CurrentAsset(m)
	if err != nil || m.Version != Version {
		add("stable-manifest", "FAIL", "release-binding", "Installer payload does not match the signed stable release", "Run csx doctor --fix; if signature verification fails, run the official installer")
		return
	}
	if installErr == nil && in.Kind == "launcher" {
		a, err := launcher.Read(in.InstallRoot)
		if err != nil || a.Current.SHA256 != asset.SHA256 || a.Current.Sequence != m.Sequence {
			add("stable-manifest", "FAIL", "release-binding", "Installer payload does not match the signed stable release", "Run csx doctor --fix; if signature verification fails, run the official installer")
			return
		}
		if runtime.GOOS == "windows" && asset.LauncherSHA256 != "" {
			hash, hashErr := doctorSHA(in.LauncherPath)
			if hashErr != nil || !strings.EqualFold(hash, asset.LauncherSHA256) {
				add("launcher-signature", "FAIL", "launcher-hash-mismatch", "Launcher differs from the signed stable release", "Run csx doctor --fix")
			} else {
				add("launcher-signature", "PASS", "launcher-signed", "Launcher matches the signed stable release", "")
				if err := doctorLauncherVersionProbe(ctx, in.LauncherPath, asset.MinLauncherVersion); err != nil {
					add("launcher-version", "FAIL", "launcher-version-invalid", "Signed launcher did not report a compatible protocol version", "Report the signed release defect to the maintainer")
				} else {
					add("launcher-version", "PASS", "launcher-version-ready", "Signed launcher reports a compatible protocol version", "")
				}
			}
		}
	} else if installErr == nil && in.Kind == "standalone" {
		hash, hashErr := doctorSHA(in.ExecutablePath)
		if hashErr != nil || !strings.EqualFold(hash, asset.SHA256) {
			add("payload", "FAIL", "payload-hash-mismatch", "Installed executable differs from the signed stable payload", "Run csx doctor --fix")
			return
		}
	}
	add("stable-manifest", "PASS", "signed-stable-binding", "Release is bound to a verified signed stable manifest", "")
}

func signedDoctorManifest(ctx context.Context, version string) (csxupdate.Manifest, error) {
	return csxupdate.VerifyInstalledStableRelease(ctx, version, doctorHTTP)
}

func probeLauncherVersion(parent context.Context, path, minimum string) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--launcher-version").Output()
	if err != nil {
		return err
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 || parts[0] != "csx-launcher" || !csxupdate.IsCanonicalReleaseVersion(parts[1]) {
		return errors.New("invalid launcher version output")
	}
	if minimum != "" {
		if cmp, err := csxupdate.CompareVersions(parts[1], minimum); err != nil || cmp < 0 {
			return errors.New("launcher protocol below signed minimum")
		}
	}
	return nil
}

func attemptRepairs(ctx context.Context, home, exe string, before doctorReport) {
	executableRegular := false
	for _, check := range before.Checks {
		if check.ID == "home" && check.Status == "FAIL" {
			return
		}
		if check.ID == "executable" && check.Status == "PASS" {
			executableRegular = true
		}
	}
	for _, check := range before.Checks {
		if check.ID == "cache-stale" && check.Code == "stale-cache-temp" {
			for _, path := range staleCacheTemps(home) {
				_ = os.Remove(path)
			}
		}
		if check.Status != "FAIL" {
			continue
		}
		if strings.HasPrefix(check.ID, "cache-") && check.Code == "cache-directory-missing" {
			_ = os.MkdirAll(filepath.Join(home, strings.TrimPrefix(check.ID, "cache-")), 0o700)
		}
	}
	for _, check := range before.Checks {
		if check.ID == "update-lock" && check.Code == "stale-update-lock" {
			_ = csxupdate.RepairStaleUpdateLock(home)
		}
		if check.ID == "local-db" && check.Code == "database-schema-stale" {
			if db, err := localdb.Open(filepath.Join(home, "csx.db")); err == nil {
				_ = db.Close()
			}
		}
		if check.ID == "mcp-config" && check.Code == "csx-block-stale" {
			if path := codexDoctorPath(); path != "" {
				_ = repairCodexDoctorBlock(path)
			}
		}
		if executableRegular && check.ID == "launcher-signature" && check.Status == "FAIL" {
			in, err := csxupdate.LoadInstall(home)
			if err == nil && in.Kind == "launcher" && doctorOwnedLauncher(in) {
				_, _ = csxupdate.RepairSignedLauncher(ctx, in.InstallRoot, Version)
			}
		}
		if executableRegular && check.ID == "payload" && check.Code == "payload-hash-mismatch" {
			_ = csxupdate.RepairSignedStandalone(ctx, home, exe, Version)
		}
	}
	var releaseBindingMismatch, payloadBroken, signedStable bool
	for _, check := range before.Checks {
		if check.Status != "FAIL" {
			continue
		}
		if (check.ID == "stable-manifest" && check.Code == "release-binding") ||
			(check.ID == "payload" && check.Code == "launcher-payload-version") ||
			(check.ID == "launcher" && check.Code == "launcher-payload-pair") {
			releaseBindingMismatch = true
		}
		if check.ID == "payload" && (check.Code == launcher.ReasonPayloadMissing || check.Code == launcher.ReasonPayloadCorrupt || check.Code == launcher.ReasonPayloadUnreadable) {
			payloadBroken = true
		}
	}
	for _, check := range before.Checks {
		if check.ID == "stable-manifest" && check.Status == "PASS" {
			signedStable = true
		}
		if check.ID == "stable-manifest" && (check.Code == "manifest-unverified" || check.Code == "release-unreachable") {
			releaseBindingMismatch = false
		}
	}
	if executableRegular && releaseBindingMismatch {
		in, err := csxupdate.LoadInstall(home)
		if err == nil && in.Kind == "launcher" && doctorOwnedLauncher(in) {
			opts := csxupdate.RehydrateOptions{Force: true, HTTP: doctorHTTP}
			_ = doctorRepairReleaseBinding(ctx, home, in.InstallRoot, Version, opts)
		}
	} else if executableRegular && payloadBroken && signedStable {
		in, err := csxupdate.LoadInstall(home)
		if err == nil && in.Kind == "launcher" && sameDoctorPath(in.ExecutablePath, exe) && doctorOwnedLauncher(in) {
			_, _ = doctorRehydrate(ctx, in.InstallRoot, csxupdate.RehydrateOptions{Force: true, HTTP: doctorHTTP})
		}
	}
}

func repairCodexDoctorBlock(path string) error {
	if cwd, err := os.Getwd(); err == nil {
		for dir := cwd; ; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				if rel, err := filepath.Rel(dir, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
					return errors.New("agent configuration is inside a project")
				}
				break
			}
			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("agent configuration is not a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	current := string(raw)
	i, j := strings.Index(current, tomlBegin), strings.Index(current, tomlEnd)
	if i < 0 || j <= i || strings.Count(current, tomlBegin) != 1 || strings.Count(current, tomlEnd) != 1 {
		return errors.New("CSX marker pair is invalid")
	}
	next := upsertMarkerBlock(current, tomlBegin, tomlEnd, codexMCPBlock())
	if next == current {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".csx-doctor-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(fi.Mode().Perm()); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := io.WriteString(f, next); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

var doctorOwnedLauncher = func(in csxupdate.Install) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	root := filepath.Clean(in.InstallRoot)
	local := os.Getenv("LOCALAPPDATA")
	if local == "" || !sameDoctorPath(root, filepath.Join(local, "csx")) || !sameDoctorPath(in.LauncherPath, filepath.Join(root, "csx.exe")) {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(root); err != nil || !sameDoctorPath(resolved, root) {
		return false
	}
	if csxupdate.SafeLauncherRepairTree(root, Version) != nil {
		return false
	}
	payloadRoot := filepath.Clean(filepath.Join(root, "payloads"))
	rel, err := filepath.Rel(payloadRoot, filepath.Clean(in.ExecutablePath))
	return err == nil && !strings.HasPrefix(rel, "..")
}

func doctorSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// The probe uses an isolated temporary CSX profile. A real MCP initialize
// response proves startup without touching the installation's database or
// launching its community daemon.
func probeMCPStartup(parent context.Context, command string) error {
	home, err := os.MkdirTemp("", "csx-doctor-mcp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, "mcp")
	cmd.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"csx-doctor\",\"version\":\"1\"}}}\n")
	env := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(key)
		if upper == "CSX_HOME" || upper == "CSX_DEBUG" || upper == "CSX_LAUNCHER_NO_REPAIR" || strings.HasPrefix(upper, "CSX_LAUNCHER_") || strings.HasPrefix(upper, "CSX_ACTIVE_") || upper == "CSX_PAYLOAD_VERSION" {
			continue
		}
		env = append(env, item)
	}
	cmd.Env = append(env, "CSX_HOME="+home, "CSX_LAUNCHER_NO_REPAIR=1")
	out, err := cmd.Output()
	if err != nil {
		return errors.New("MCP child failed")
	}
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		var response struct {
			Result struct {
				ServerInfo struct {
					Name string `json:"name"`
				} `json:"serverInfo"`
			} `json:"result"`
		}
		if json.Unmarshal(line, &response) == nil && response.Result.ServerInfo.Name == "codesamplex" {
			return nil
		}
	}
	return errors.New("MCP initialize response missing")
}

// A host owns its live MCP child. Doctor reports stale sessions but does not
// terminate a process another application may still be using.
func inspectMCPProcesses(ctx context.Context, expected string) (bool, bool) {
	current := func(path string) bool {
		if sameDoctorPath(path, expected) {
			return true
		}
		if home, err := config.Home(); err == nil {
			if in, err := csxupdate.LoadInstall(home); err == nil && in.Kind == "launcher" {
				if a, err := launcher.Read(in.InstallRoot); err == nil {
					if payload, err := launcher.PayloadPath(in.InstallRoot, a.Current.Version); err == nil && sameDoctorPath(path, payload) {
						return true
					}
				}
			}
		}
		return false
	}
	if runtime.GOOS == "linux" {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return false, false
		}
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid == os.Getpid() {
				continue
			}
			base := filepath.Join("/proc", entry.Name())
			raw, err := os.ReadFile(filepath.Join(base, "cmdline"))
			if err != nil {
				continue
			}
			parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
			if len(parts) < 2 || parts[1] != "mcp" || !strings.HasPrefix(filepath.Base(parts[0]), "csx") {
				continue
			}
			path, err := os.Readlink(filepath.Join(base, "exe"))
			if err != nil {
				continue
			}
			if !current(path) {
				return true, true
			}
			stat, err := os.ReadFile(filepath.Join(base, "stat"))
			if err == nil {
				if i := strings.LastIndex(string(stat), ") "); i >= 0 {
					fields := strings.Fields(string(stat[i+2:]))
					if len(fields) > 1 && fields[1] == "1" {
						return true, true
					}
				}
			}
		}
		return false, true
	}
	if runtime.GOOS == "windows" {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		const query = `$all=Get-CimInstance Win32_Process;$ids=@{};foreach($p in $all){$ids[[int]$p.ProcessId]=$true};$all|Where-Object {$_.Name -eq 'csx.exe' -or $_.Name -eq 'csx-payload.exe'}|ForEach-Object {[pscustomobject]@{ExecutablePath=$_.ExecutablePath;CommandLine=$_.CommandLine;Orphan=(-not $ids.ContainsKey([int]$_.ParentProcessId))}}|ConvertTo-Json -Compress`
		cmd := exec.CommandContext(probeCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", query)
		out, err := cmd.Output()
		if err != nil {
			return false, false
		}
		var rows []struct {
			ExecutablePath string
			CommandLine    string
			Orphan         bool
		}
		if len(bytes.TrimSpace(out)) == 0 {
			return false, true
		}
		if bytes.HasPrefix(bytes.TrimSpace(out), []byte{'{'}) {
			var one struct {
				ExecutablePath string
				CommandLine    string
				Orphan         bool
			}
			if json.Unmarshal(out, &one) != nil {
				return false, false
			}
			rows = append(rows, one)
		} else if json.Unmarshal(out, &rows) != nil {
			return false, false
		}
		for _, row := range rows {
			if strings.Contains(strings.ToLower(row.CommandLine), " mcp") && (row.Orphan || !current(row.ExecutablePath)) {
				return true, true
			}
		}
		return false, true
	}
	return false, false
}
