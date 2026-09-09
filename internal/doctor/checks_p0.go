package doctor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/launcher"
	"github.com/r2cuerdame/codesamplex/internal/update"
	_ "modernc.org/sqlite"
)

var canonicalVersionRegex = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// -----------------------------------------------------------------------------
// P0: Launcher Integrity
// -----------------------------------------------------------------------------

type LauncherIntegrityCheck struct{}

func (c *LauncherIntegrityCheck) ID() string { return "p0.launcher.integrity" }
func (c *LauncherIntegrityCheck) Tier() Tier  { return TierP0 }

func (c *LauncherIntegrityCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	exeName := "csx"
	if runtime.GOOS == "windows" {
		exeName = "csx.exe"
	}
	launcherPath := filepath.Join(cctx.Root, exeName)

	fi, err := os.Lstat(launcherPath)
	if err != nil {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("launcher binary missing at %s", launcherPath)
		d.Remediation = "Reinstall CodeSampleX via the official installer or run 'csx doctor --fix'"
		d.Error = err.Error()
		// If a previous launcher or aside exists, it is fixable
		if hasPreviousLauncher(cctx.Root, exeName) {
			d.Fixable = true
		}
		return d
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("launcher binary at %s is a symlink; regular file required", launcherPath)
		d.Remediation = "Replace the symlink with the genuine csx executable"
		return d
	}

	if !fi.Mode().IsRegular() {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("launcher at %s is not a regular file", launcherPath)
		d.Remediation = "Ensure the launcher path is a standard executable file"
		return d
	}

	// Protocol self-test probe
	if cctx.CommandRunner != nil {
		out, err := cctx.CommandRunner(ctx, launcherPath, "--launcher-version")
		if err != nil {
			d.Status = StatusFail
			d.Summary = fmt.Sprintf("launcher self-test failed: %v", err)
			d.Details = append(d.Details, strings.TrimSpace(string(out)))
			d.Remediation = "Verify file permissions and executable integrity"
			d.Error = err.Error()
			if hasPreviousLauncher(cctx.Root, exeName) {
				d.Fixable = true
			}
			return d
		}
		verStr := strings.TrimSpace(string(out))
		if !strings.HasPrefix(verStr, "csx-launcher ") {
			d.Status = StatusFail
			d.Summary = fmt.Sprintf("launcher returned unexpected protocol output: %q", verStr)
			d.Remediation = "The launcher executable is corrupted or obsolete; reinstall"
			return d
		}
		d.Summary = fmt.Sprintf("launcher binary verified (%s, protocol %s)", launcherPath, launcher.ProtocolVersion)
		d.Details = append(d.Details, fmt.Sprintf("probe output: %s", verStr))
		return d
	}

	d.Summary = fmt.Sprintf("launcher binary exists as regular file (%s)", launcherPath)
	return d
}

func (c *LauncherIntegrityCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	exeName := "csx"
	if runtime.GOOS == "windows" {
		exeName = "csx.exe"
	}
	target := filepath.Join(cctx.Root, exeName)

	prev := findBestPreviousLauncher(cctx.Root, exeName)
	if prev == "" {
		return diag, errors.New("no previous launcher candidate available to restore")
	}

	// Verify the previous launcher can run if command runner available
	if cctx.CommandRunner != nil {
		if out, err := cctx.CommandRunner(ctx, prev, "--launcher-version"); err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), "csx-launcher ") {
			return diag, fmt.Errorf("previous launcher at %s failed self-test: %w", prev, err)
		}
	}

	// Copy/rename candidate to target
	data, err := os.ReadFile(prev)
	if err != nil {
		return diag, fmt.Errorf("read previous launcher: %w", err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		return diag, fmt.Errorf("restore launcher: %w", err)
	}

	return c.Diagnose(ctx, cctx), nil
}

func hasPreviousLauncher(root, exeName string) bool {
	return findBestPreviousLauncher(root, exeName) != ""
}

func findBestPreviousLauncher(root, exeName string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var bestPath string
	var bestMod time.Time
	prefixes := []string{
		"csx.previous-", "csx.exe.previous-",
		"csx.old-", "csx.exe.old-",
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		matched := false
		for _, pfx := range prefixes {
			if strings.HasPrefix(name, pfx) {
				matched = true
				break
			}
		}
		if matched {
			p := filepath.Join(root, name)
			fi, err := os.Stat(p)
			if err == nil && fi.ModTime().After(bestMod) {
				bestMod = fi.ModTime()
				bestPath = p
			}
		}
	}
	return bestPath
}

// -----------------------------------------------------------------------------
// P0: Payload Verification
// -----------------------------------------------------------------------------

type PayloadVerificationCheck struct{}

func (c *PayloadVerificationCheck) ID() string { return "p0.payload.verification" }
func (c *PayloadVerificationCheck) Tier() Tier  { return TierP0 }

func (c *PayloadVerificationCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	act, err := launcher.Read(cctx.Root)
	if err != nil {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("active descriptor active.json unreadable: %v", err)
		d.Remediation = "Repair active.json pointer using 'csx doctor --fix' or reinstall"
		d.Error = err.Error()
		d.Fixable = true
		return d
	}

	if act.Current.Version == "" || act.Current.SHA256 == "" {
		d.Status = StatusFail
		d.Summary = "active descriptor does not specify a valid current payload"
		d.Remediation = "A valid payload version and SHA256 must be committed"
		if act.Previous != nil && act.Previous.Version != "" {
			d.Fixable = true
		}
		return d
	}

	payloadPath, err := launcher.PayloadPath(cctx.Root, act.Current.Version)
	if err != nil {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("invalid payload path: %v", err)
		d.Error = err.Error()
		return d
	}

	if verr := launcher.VerifyPayload(cctx.Root, act.Current); verr != nil {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("payload verification failed for %s: %v", act.Current.Version, verr)
		d.Details = append(d.Details, fmt.Sprintf("expected SHA256: %s", act.Current.SHA256))
		d.Details = append(d.Details, fmt.Sprintf("payload path: %s", payloadPath))
		d.Remediation = "Run 'csx doctor --fix' to rollback to previous verified payload or rehydrate"
		d.Error = verr.Error()
		if act.Previous != nil && launcher.VerifyPayload(cctx.Root, *act.Previous) == nil {
			d.Fixable = true
		}
		return d
	}

	if cctx.CommandRunner != nil {
		out, err := cctx.CommandRunner(ctx, payloadPath, "version")
		if err != nil {
			d.Status = StatusFail
			d.Summary = fmt.Sprintf("payload executable self-test failed: %v", err)
			d.Details = append(d.Details, strings.TrimSpace(string(out)))
			d.Remediation = "Payload binary cannot execute; rollback via 'csx doctor --fix'"
			d.Error = err.Error()
			if act.Previous != nil && launcher.VerifyPayload(cctx.Root, *act.Previous) == nil {
				d.Fixable = true
			}
			return d
		}
	}

	d.Summary = fmt.Sprintf("active payload verified (%s, sha256:%s)", act.Current.Version, act.Current.SHA256[:min(12, len(act.Current.SHA256))])
	d.Details = append(d.Details, fmt.Sprintf("path: %s", payloadPath))
	return d
}

func (c *PayloadVerificationCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	act, err := launcher.Read(cctx.Root)
	if err != nil {
		// Attempt load fallback
		loaded, lerr := launcher.Load(cctx.Root)
		if lerr != nil {
			return diag, fmt.Errorf("cannot recover active descriptor: %w", lerr)
		}
		act = loaded
	}

	if act.Previous != nil && launcher.VerifyPayload(cctx.Root, *act.Previous) == nil {
		if _, rerr := launcher.Rollback(cctx.Root); rerr != nil {
			return diag, fmt.Errorf("rollback failed: %w", rerr)
		}
		return c.Diagnose(ctx, cctx), nil
	}

	return diag, errors.New("no verified fallback payload available for automatic rollback")
}

// -----------------------------------------------------------------------------
// P0: Launcher Consistency
// -----------------------------------------------------------------------------

type LauncherConsistencyCheck struct{}

func (c *LauncherConsistencyCheck) ID() string { return "p0.launcher.consistency" }
func (c *LauncherConsistencyCheck) Tier() Tier  { return TierP0 }

func (c *LauncherConsistencyCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	activePath := launcher.Path(cctx.Root)
	raw, err := os.ReadFile(activePath)
	if err != nil {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("active.json missing or unreadable: %v", err)
		d.Error = err.Error()
		d.Remediation = "Restore active.json from verified install descriptor"
		return d
	}

	var act launcher.Active
	if err := json.Unmarshal(raw, &act); err != nil {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("active.json JSON syntax error: %v", err)
		d.Remediation = "Correct active.json syntax or run 'csx doctor --fix'"
		d.Error = err.Error()
		d.Fixable = true
		return d
	}

	if act.Schema != launcher.Schema {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("active.json schema mismatch: got %d, expected %d", act.Schema, launcher.Schema)
		d.Remediation = "Upgrade launcher configuration schema"
		return d
	}

	if !canonicalVersionRegex.MatchString(act.Current.Version) {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("current version %q is not canonical vX.Y.Z format", act.Current.Version)
		return d
	}

	if act.Previous != nil {
		if !canonicalVersionRegex.MatchString(act.Previous.Version) {
			d.Status = StatusFail
			d.Summary = fmt.Sprintf("previous version %q is not canonical vX.Y.Z format", act.Previous.Version)
			return d
		}
		if act.Previous.Version == act.Current.Version && act.Previous.SHA256 == act.Current.SHA256 {
			d.Status = StatusWarn
			d.Summary = "active descriptor records identical current and previous payload descriptors"
		}
	}

	// Check environment drift
	if cctx.GetEnv != nil {
		if envHome := cctx.GetEnv("CSX_HOME"); envHome != "" && cctx.Home != "" {
			if cleanA, cleanB := filepath.Clean(envHome), filepath.Clean(cctx.Home); cleanA != cleanB {
				d.Status = StatusWarn
				d.Details = append(d.Details, fmt.Sprintf("CSX_HOME env (%s) differs from resolved home (%s)", cleanA, cleanB))
			}
		}
	}

	if d.Status == StatusPass {
		d.Summary = fmt.Sprintf("launcher descriptor consistent (schema %d, version %s)", act.Schema, act.Current.Version)
	}
	return d
}

func (c *LauncherConsistencyCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	_, err := launcher.Load(cctx.Root)
	if err != nil {
		return diag, fmt.Errorf("heal active.json failed: %w", err)
	}
	return c.Diagnose(ctx, cctx), nil
}

// -----------------------------------------------------------------------------
// P0: Manifest Release Binding
// -----------------------------------------------------------------------------

type ManifestReleaseBindingCheck struct{}

func (c *ManifestReleaseBindingCheck) ID() string { return "p0.manifest.release_binding" }
func (c *ManifestReleaseBindingCheck) Tier() Tier  { return TierP0 }

func (c *ManifestReleaseBindingCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	manifestStaged := filepath.Join(cctx.Root, "csx-manifest.new.json")
	bootstrapStaged := filepath.Join(cctx.Root, "csx-bootstrap.new.json")

	stagedManifestExists := fileExists(manifestStaged)
	stagedBootstrapExists := fileExists(bootstrapStaged)

	if stagedManifestExists || stagedBootstrapExists {
		d.Fixable = true
		if !stagedManifestExists || !stagedBootstrapExists {
			d.Status = StatusFail
			d.Summary = "incomplete staged bootstrap manifests detected in root"
			d.Remediation = "Run 'csx doctor --fix' to purge broken installer artifacts"
			return d
		}

		stableRaw, err1 := os.ReadFile(manifestStaged)
		bootstrapRaw, err2 := os.ReadFile(bootstrapStaged)
		if err1 != nil || err2 != nil {
			d.Status = StatusFail
			d.Summary = "staged bootstrap manifest files could not be read"
			d.Remediation = "Run 'csx doctor --fix' to purge unreadable staged files"
			return d
		}

		var env1, env2 struct {
			Payload   string `json:"payload"`
			Signature string `json:"signature"`
		}
		if err := json.Unmarshal(stableRaw, &env1); err != nil {
			d.Status = StatusFail
			d.Summary = fmt.Sprintf("staged release manifest is invalid JSON: %v", err)
			d.Remediation = "Run 'csx doctor --fix' to purge corrupted staged manifests"
			d.Error = err.Error()
			return d
		}
		if err := json.Unmarshal(bootstrapRaw, &env2); err != nil {
			d.Status = StatusFail
			d.Summary = fmt.Sprintf("staged bootstrap manifest is invalid JSON: %v", err)
			d.Remediation = "Run 'csx doctor --fix' to purge corrupted staged manifests"
			d.Error = err.Error()
			return d
		}

		pub, err := update.EmbeddedPublicKey()
		if err == nil {
			act, _ := launcher.Read(cctx.Root)
			currVer := act.Current.Version
			now := time.Now().UTC()
			if cctx.Now != nil {
				now = cctx.Now().UTC()
			}
			_, verr := update.VerifyBootstrapRelease(stableRaw, bootstrapRaw, pub, now, currVer)
			if verr != nil {
				d.Status = StatusFail
				if strings.Contains(verr.Error(), "installer payload does not match the signed stable release") {
					d.Summary = "installer payload does not match the signed stable release"
					d.Remediation = "Run 'csx doctor --fix' to clear mismatched staged installer assets"
				} else {
					d.Summary = fmt.Sprintf("staged release manifest verification failed: %v", verr)
					d.Remediation = "Run 'csx doctor --fix' to purge corrupted staged manifests"
				}
				d.Error = verr.Error()
				return d
			}
		}
	}

	d.Summary = "release manifest binding and bootstrap envelopes consistent"
	return d
}

func (c *ManifestReleaseBindingCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	stagedFiles := []string{
		filepath.Join(cctx.Root, "csx-manifest.new.json"),
		filepath.Join(cctx.Root, "csx-bootstrap.new.json"),
		filepath.Join(cctx.Root, "csx-payload.new.exe"),
	}
	var errs []string
	for _, f := range stagedFiles {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("%s: %v", filepath.Base(f), err))
		}
	}
	if len(errs) > 0 {
		return diag, errors.New("failed to remove staged files: " + strings.Join(errs, ", "))
	}
	return c.Diagnose(ctx, cctx), nil
}

// -----------------------------------------------------------------------------
// P0: MCP Viability
// -----------------------------------------------------------------------------

type MCPViabilityCheck struct{}

func (c *MCPViabilityCheck) ID() string { return "p0.mcp.viability" }
func (c *MCPViabilityCheck) Tier() Tier  { return TierP0 }

func (c *MCPViabilityCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	exeName := "csx"
	if runtime.GOOS == "windows" {
		exeName = "csx.exe"
	}
	targetExe := filepath.Join(cctx.Root, exeName)
	if !fileExists(targetExe) {
		if exe, err := os.Executable(); err == nil {
			targetExe = exe
		}
	}

	if !fileExists(targetExe) {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("MCP server executable not found at %s", targetExe)
		d.Remediation = "Install CodeSampleX or correct install root"
		return d
	}

	if cctx.CommandRunner != nil {
		out, err := cctx.CommandRunner(ctx, targetExe, "mcp-config", "--path")
		if err != nil {
			d.Status = StatusFail
			d.Summary = fmt.Sprintf("MCP viability check failed: %v", err)
			d.Details = append(d.Details, strings.TrimSpace(string(out)))
			d.Remediation = "Verify that csx mcp runs properly and dependencies are satisfied"
			d.Error = err.Error()
			return d
		}
		pathReported := strings.TrimSpace(string(out))
		d.Summary = fmt.Sprintf("MCP stdio server viable (%s)", targetExe)
		d.Details = append(d.Details, fmt.Sprintf("mcp path: %s", pathReported))
		return d
	}

	d.Summary = fmt.Sprintf("MCP executable exists at %s", targetExe)
	return d
}

func (c *MCPViabilityCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	return diag, errors.New("mcp viability requires executable repair via launcher/payload repair")
}

// -----------------------------------------------------------------------------
// P0: Server Compatibility
// -----------------------------------------------------------------------------

type ServerCompatibilityCheck struct{}

func (c *ServerCompatibilityCheck) ID() string { return "p0.server.compatibility" }
func (c *ServerCompatibilityCheck) Tier() Tier  { return TierP0 }

type serverVersionResp struct {
	Service     string `json:"service"`
	Version     string `json:"version"`
	Revision    string `json:"revision"`
	Environment string `json:"environment"`
}

func (c *ServerCompatibilityCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	if cctx.Config != nil && cctx.Config.Mode == config.ModeLocalOnly {
		d.Summary = "local-only mode: server connection not used (privacy policy)"
		return d
	}

	serverURL := "https://codesamplex.dev"
	if cctx.Config != nil && cctx.Config.ServerURL != "" {
		serverURL = cctx.Config.ServerURL
	}
	serverURL = strings.TrimRight(serverURL, "/")

	client := cctx.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	// GET /version
	reqVer, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/version", nil)
	if err != nil {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("invalid server URL %s: %v", serverURL, err)
		return d
	}
	respVer, err := client.Do(reqVer)
	if err != nil {
		// Network unreachable
		if serverURL == "https://codesamplex.dev" {
			d.Status = StatusWarn
			d.Summary = fmt.Sprintf("server unreachable at %s (network offline or restricted): %v", serverURL, err)
			d.Remediation = "Verify internet access or configure a custom serverUrl"
			return d
		}
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("configured server unreachable at %s: %v", serverURL, err)
		d.Remediation = "Ensure the custom serverUrl is running and network route is open"
		d.Error = err.Error()
		return d
	}
	defer respVer.Body.Close()

	if respVer.StatusCode != http.StatusOK {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("server /version returned HTTP %d", respVer.StatusCode)
		return d
	}

	var verData serverVersionResp
	body, _ := io.ReadAll(io.LimitReader(respVer.Body, 8192))
	if err := json.Unmarshal(body, &verData); err != nil {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("server /version returned invalid JSON: %v", err)
		return d
	}

	if verData.Service != "csx-server" {
		d.Status = StatusFail
		d.Summary = fmt.Sprintf("incompatible server service: expected 'csx-server', got %q", verData.Service)
		d.Remediation = "Point csx to a valid CodeSampleX server instance"
		return d
	}

	// GET /healthz
	reqHealth, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/healthz", nil)
	if err == nil {
		respHealth, err := client.Do(reqHealth)
		if err == nil {
			defer respHealth.Body.Close()
			healthBody, _ := io.ReadAll(io.LimitReader(respHealth.Body, 1024))
			if respHealth.StatusCode != http.StatusOK || !strings.Contains(string(healthBody), "ok") {
				d.Status = StatusFail
				d.Summary = fmt.Sprintf("server /healthz reported unhealthy status %d: %s", respHealth.StatusCode, strings.TrimSpace(string(healthBody)))
				d.Remediation = "Check server logs and database connectivity on the host"
				return d
			}
		}
	}

	d.Summary = fmt.Sprintf("server compatibility verified: %s (version: %s, rev: %s)", verData.Service, verData.Version, verData.Revision)
	d.Details = append(d.Details, fmt.Sprintf("server: %s", serverURL))
	return d
}

func (c *ServerCompatibilityCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	return diag, errors.New("server compatibility is an external service condition and cannot be locally repaired")
}

// -----------------------------------------------------------------------------
// P0: Storage LocalDB
// -----------------------------------------------------------------------------

type StorageLocalDBCheck struct{}

func (c *StorageLocalDBCheck) ID() string { return "p0.storage.localdb" }
func (c *StorageLocalDBCheck) Tier() Tier  { return TierP0 }

func (c *StorageLocalDBCheck) Diagnose(ctx context.Context, cctx *CheckContext) Diagnosis {
	d := Diagnosis{
		ID:      c.ID(),
		Tier:    c.Tier(),
		Status:  StatusPass,
		Fixable: false,
	}

	// 1. Structure check
	missingDirs := false
	for _, sub := range []string{"", "cas", "samples", "logs"} {
		path := filepath.Join(cctx.Home, sub)
		fi, err := os.Stat(path)
		if err != nil || !fi.IsDir() {
			missingDirs = true
			d.Details = append(d.Details, fmt.Sprintf("missing directory: %s", path))
		}
	}

	if missingDirs {
		d.Status = StatusFail
		d.Summary = "$CSX_HOME directory structure is incomplete"
		d.Remediation = "Run 'csx doctor --fix' to create required storage directories"
		d.Fixable = true
		return d
	}

	// 2. SQLite csx.db integrity check
	dbPath := filepath.Join(cctx.Home, "csx.db")
	if fileExists(dbPath) {
		dsn := "file:" + filepath.ToSlash(dbPath) + "?_pragma=busy_timeout(5000)"
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			d.Status = StatusFail
			d.Summary = fmt.Sprintf("cannot open csx.db: %v", err)
			d.Fixable = true
			d.Error = err.Error()
			return d
		}
		defer db.Close()

		var result string
		qerr := db.QueryRowContext(ctx, "PRAGMA integrity_check(1);").Scan(&result)
		if qerr != nil {
			d.Status = StatusFail
			d.Summary = fmt.Sprintf("csx.db PRAGMA integrity_check failed: %v", qerr)
			d.Remediation = "Run 'csx doctor --fix' to repair or recreate corrupted SQLite database"
			d.Fixable = true
			d.Error = qerr.Error()
			return d
		}

		if result != "ok" {
			d.Status = StatusFail
			d.Summary = fmt.Sprintf("csx.db corruption detected: %s", result)
			d.Remediation = "Run 'csx doctor --fix' to isolate corrupted database and initialize clean store"
			d.Fixable = true
			return d
		}
		d.Summary = "storage directory structure and csx.db SQLite integrity verified (ok)"
		return d
	}

	d.Summary = "$CSX_HOME storage structure valid (csx.db uninitialized)"
	return d
}

func (c *StorageLocalDBCheck) Repair(ctx context.Context, cctx *CheckContext, diag Diagnosis) (Diagnosis, error) {
	if err := config.EnsureHome(cctx.Home); err != nil {
		return diag, fmt.Errorf("ensure home: %w", err)
	}

	dbPath := filepath.Join(cctx.Home, "csx.db")
	if fileExists(dbPath) {
		// Test integrity again
		dsn := "file:" + filepath.ToSlash(dbPath) + "?_pragma=busy_timeout(5000)"
		db, err := sql.Open("sqlite", dsn)
		corrupt := false
		if err == nil {
			var result string
			if qerr := db.QueryRowContext(ctx, "PRAGMA integrity_check(1);").Scan(&result); qerr != nil || result != "ok" {
				corrupt = true
			}
			db.Close()
		} else {
			corrupt = true
		}

		if corrupt {
			stamp := time.Now().Format("20060102150405")
			_ = os.Rename(dbPath, dbPath+".corrupt-"+stamp)
			_ = os.Rename(dbPath+"-wal", dbPath+"-wal.corrupt-"+stamp)
			_ = os.Rename(dbPath+"-shm", dbPath+"-shm.corrupt-"+stamp)
		}
	}

	return c.Diagnose(ctx, cctx), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
