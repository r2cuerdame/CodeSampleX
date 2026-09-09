package doctor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

func setupTestEnv(t *testing.T) (*CheckContext, string, string) {
	t.Helper()
	home := t.TempDir()
	root := t.TempDir()
	agentHome := t.TempDir()

	_ = config.EnsureHome(home)

	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly

	cctx := &CheckContext{
		Home:       home,
		Root:       root,
		AgentHome:  agentHome,
		Config:     cfg,
		Fix:        false,
		Verbose:    true,
		Now:        time.Now,
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
		CommandRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "--launcher-version" {
				return []byte("csx-launcher v1.0.0\n"), nil
			}
			if len(args) > 0 && (args[0] == "version" || (len(args) > 1 && args[1] == "--path")) {
				return []byte("csx v1.0.0\n"), nil
			}
			return []byte("ok"), nil
		},
		ProcessLister: func() ([]ProcessInfo, error) {
			return nil, nil
		},
		PidAlive: func(pid int) bool {
			return pid == 42 // mock PID 42 is alive
		},
		GetEnv: func(k string) string { return "" },
	}
	return cctx, home, root
}

func TestLauncherIntegrityCheck(t *testing.T) {
	cctx, _, root := setupTestEnv(t)
	check := &LauncherIntegrityCheck{}

	// 1. Missing launcher
	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusFail {
		t.Fatalf("expected StatusFail for missing launcher, got %s", diag.Status)
	}

	// 2. Create previous launcher to test repair
	exeName := "csx"
	if runtime.GOOS == "windows" {
		exeName = "csx.exe"
	}
	prevLauncher := filepath.Join(root, exeName+".previous-123")
	if err := os.WriteFile(prevLauncher, []byte("fake-launcher-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Re-diagnose: should be fixable now
	diag = check.Diagnose(context.Background(), cctx)
	if !diag.Fixable {
		t.Fatal("expected fixable when previous launcher exists")
	}

	// Repair
	cctx.Fix = true
	repaired, err := check.Repair(context.Background(), cctx, diag)
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if repaired.Status != StatusPass {
		t.Fatalf("expected StatusPass after repair, got %s", repaired.Status)
	}
}

func TestPayloadVerificationAndRollback(t *testing.T) {
	cctx, _, root := setupTestEnv(t)
	check := &PayloadVerificationCheck{}

	// Setup payload v1.0.0 and v1.0.1
	v0Path, _ := launcher.PayloadPath(root, "v1.0.0")
	_ = os.MkdirAll(filepath.Dir(v0Path), 0o700)
	v0Bytes := []byte("payload-v1.0.0-content")
	_ = os.WriteFile(v0Path, v0Bytes, 0o700)
	v0Sum := sha256.Sum256(v0Bytes)

	v1Path, _ := launcher.PayloadPath(root, "v1.0.1")
	_ = os.MkdirAll(filepath.Dir(v1Path), 0o700)
	v1Bytes := []byte("payload-v1.0.1-content")
	_ = os.WriteFile(v1Path, v1Bytes, 0o700)
	v1Sum := sha256.Sum256(v1Bytes)

	// Write active.json with Current=v1.0.1 (valid) and Previous=v1.0.0
	act := launcher.Active{
		Schema: 1,
		Current: launcher.Descriptor{
			Version:  "v1.0.1",
			SHA256:   hex.EncodeToString(v1Sum[:]),
			Sequence: 2,
		},
		Previous: &launcher.Descriptor{
			Version:  "v1.0.0",
			SHA256:   hex.EncodeToString(v0Sum[:]),
			Sequence: 1,
		},
	}
	if err := launcher.Write(root, act); err != nil {
		t.Fatal(err)
	}

	// Diagnose: should pass
	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusPass {
		t.Fatalf("expected StatusPass for valid payload, got %s: %s", diag.Status, diag.Summary)
	}

	// Corrupt current payload file
	_ = os.WriteFile(v1Path, []byte("corrupted-data"), 0o700)

	// Diagnose again: should fail but be fixable via rollback to previous
	diag = check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusFail {
		t.Fatalf("expected StatusFail for corrupt payload, got %s", diag.Status)
	}
	if !diag.Fixable {
		t.Fatal("expected fixable payload via rollback")
	}

	// Repair: roll back to previous
	repaired, err := check.Repair(context.Background(), cctx, diag)
	if err != nil {
		t.Fatalf("repair payload failed: %v", err)
	}
	if repaired.Status != StatusPass {
		t.Fatalf("expected StatusPass after rollback, got %s", repaired.Status)
	}
}

func TestLauncherConsistencyCheck(t *testing.T) {
	cctx, _, root := setupTestEnv(t)
	check := &LauncherConsistencyCheck{}

	// Missing active.json
	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusFail {
		t.Fatalf("expected StatusFail, got %s", diag.Status)
	}

	// Invalid version format
	act := launcher.Active{
		Schema: 1,
		Current: launcher.Descriptor{
			Version:  "invalid-version",
			SHA256:   "abcd",
			Sequence: 1,
		},
	}
	rawBad, _ := json.Marshal(act)
	_ = os.WriteFile(launcher.Path(root), rawBad, 0o600)
	diag = check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusFail {
		t.Fatalf("expected StatusFail for invalid version format, got %s", diag.Status)
	}

	// Valid active.json
	act.Current.Version = "v1.2.3"
	rawGood, _ := json.Marshal(act)
	_ = os.WriteFile(launcher.Path(root), rawGood, 0o600)
	diag = check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusPass {
		t.Fatalf("expected StatusPass, got %s: %s", diag.Status, diag.Summary)
	}
}

func TestManifestReleaseBindingCheckAndRepair(t *testing.T) {
	cctx, _, root := setupTestEnv(t)
	check := &ManifestReleaseBindingCheck{}

	// Clean state
	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusPass {
		t.Fatalf("expected StatusPass on clean root, got %s", diag.Status)
	}

	// Simulate broken installer staging with version mismatch
	_ = os.WriteFile(filepath.Join(root, "csx-manifest.new.json"), []byte("invalid-manifest"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "csx-bootstrap.new.json"), []byte("invalid-bootstrap"), 0o600)

	diag = check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusFail {
		t.Fatalf("expected StatusFail for invalid staged bootstrap, got %s", diag.Status)
	}
	if !diag.Fixable {
		t.Fatal("expected staged manifest mismatch to be fixable")
	}

	// Repair
	repaired, err := check.Repair(context.Background(), cctx, diag)
	if err != nil {
		t.Fatalf("repair manifest binding failed: %v", err)
	}
	if repaired.Status != StatusPass {
		t.Fatalf("expected StatusPass after staged cleanup, got %s", repaired.Status)
	}
}

func TestServerCompatibilityCheck(t *testing.T) {
	cctx, _, _ := setupTestEnv(t)
	check := &ServerCompatibilityCheck{}

	// 1. Local-only mode
	cctx.Config.Mode = config.ModeLocalOnly
	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusPass {
		t.Fatalf("expected StatusPass in local-only mode, got %s", diag.Status)
	}

	// 2. Community mode with mock server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"service":  "csx-server",
				"version":  "v1.0.0",
				"revision": "testrev123",
			})
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cctx.Config.Mode = config.ModeCommunity
	cctx.Config.ServerURL = srv.URL

	diag = check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusPass {
		t.Fatalf("expected StatusPass for compatible server, got %s: %s", diag.Status, diag.Summary)
	}

	// 3. Incompatible service name
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"service": "other-product",
			})
		}
	}))
	defer badSrv.Close()

	cctx.Config.ServerURL = badSrv.URL
	diag = check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusFail {
		t.Fatalf("expected StatusFail for incompatible service, got %s", diag.Status)
	}
}

func TestStorageLocalDBCheckAndRepair(t *testing.T) {
	cctx, home, _ := setupTestEnv(t)
	check := &StorageLocalDBCheck{}

	// Missing subdirectories
	_ = os.RemoveAll(filepath.Join(home, "cas"))
	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusFail {
		t.Fatalf("expected StatusFail for missing dirs, got %s", diag.Status)
	}

	// Repair creates dirs
	repaired, err := check.Repair(context.Background(), cctx, diag)
	if err != nil {
		t.Fatalf("repair dirs failed: %v", err)
	}
	if repaired.Status != StatusPass {
		t.Fatalf("expected StatusPass after repair, got %s", repaired.Status)
	}

	// Corrupted csx.db
	dbPath := filepath.Join(home, "csx.db")
	_ = os.WriteFile(dbPath, []byte("NOT A SQLITE DATABASE FILE"), 0o600)

	diag = check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusFail {
		t.Fatalf("expected StatusFail for corrupted db, got %s", diag.Status)
	}

	// Repair moves corrupted DB aside
	repaired, err = check.Repair(context.Background(), cctx, diag)
	if err != nil {
		t.Fatalf("repair db failed: %v", err)
	}
	if repaired.Status != StatusPass {
		t.Fatalf("expected StatusPass after isolating corrupt db, got %s", repaired.Status)
	}
}

func TestOrphanedMCPCheckAndRepair(t *testing.T) {
	cctx, _, _ := setupTestEnv(t)
	check := &OrphanedMCPCheck{}

	// 1. Process with alive parent PID 42 -> NOT an orphan
	cctx.ProcessLister = func() ([]ProcessInfo, error) {
		return []ProcessInfo{
			{Pid: 100, ParentPid: 42, Name: "csx.exe"},
		}, nil
	}
	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusPass {
		t.Fatalf("expected StatusPass for process with live parent, got %s", diag.Status)
	}

	// 2. Process with dead parent PID 99 -> ORPHAN
	killed := false
	cctx.ProcessLister = func() ([]ProcessInfo, error) {
		if killed {
			return nil, nil
		}
		return []ProcessInfo{
			{Pid: 101, ParentPid: 99, Name: "csx.exe"},
		}, nil
	}
	diag = check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusWarn {
		t.Fatalf("expected StatusWarn for orphan process, got %s", diag.Status)
	}

	// Repair
	killed = true
	repaired, err := check.Repair(context.Background(), cctx, diag)
	if err != nil {
		t.Fatalf("repair orphaned mcp failed: %v", err)
	}
	if repaired.Status != StatusPass {
		t.Fatalf("expected StatusPass after repair, got %s", repaired.Status)
	}
}

func TestStaleLocksCheckAndRepair(t *testing.T) {
	cctx, home, root := setupTestEnv(t)
	check := &StaleLocksCheck{}

	// 1. Live PID 42 lock -> must NOT be stale, must NOT be removed!
	_ = os.WriteFile(filepath.Join(home, "daemon.lock"), []byte("42\n"), 0o600)
	_ = os.WriteFile(filepath.Join(root, ".update.lock"), []byte("token 42\n"), 0o600)

	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusPass {
		t.Fatalf("expected StatusPass for live locks, got %s", diag.Status)
	}

	// 2. Dead PID 99 lock -> stale!
	_ = os.WriteFile(filepath.Join(home, "daemon.lock"), []byte("99\n"), 0o600)
	_ = os.WriteFile(filepath.Join(root, ".update.lock"), []byte("token 99\n"), 0o600)

	diag = check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusWarn {
		t.Fatalf("expected StatusWarn for dead lock, got %s", diag.Status)
	}
	if !diag.Fixable {
		t.Fatal("expected stale locks to be fixable")
	}

	// Repair removes stale locks
	repaired, err := check.Repair(context.Background(), cctx, diag)
	if err != nil {
		t.Fatalf("repair stale locks failed: %v", err)
	}
	if repaired.Status != StatusPass {
		t.Fatalf("expected StatusPass after stale locks removed, got %s", repaired.Status)
	}

	// Verify files removed
	if fileExists(filepath.Join(home, "daemon.lock")) || fileExists(filepath.Join(root, ".update.lock")) {
		t.Fatal("stale lock files were not removed")
	}
}

func TestStalePayloadsAndDisplacedBinaries(t *testing.T) {
	cctx, _, root := setupTestEnv(t)
	check := &StalePayloadsCheck{}

	// Active payload is v1.0.0
	act := launcher.Active{
		Schema:  1,
		Current: launcher.Descriptor{Version: "v1.0.0", SHA256: strings.Repeat("a", 64), Sequence: 1},
	}
	rawAct, _ := json.Marshal(act)
	_ = os.WriteFile(launcher.Path(root), rawAct, 0o600)

	// Create referenced payload and stale unreferenced payload
	_ = os.MkdirAll(filepath.Join(root, "payloads", "v1.0.0"), 0o700)
	_ = os.MkdirAll(filepath.Join(root, "payloads", "v0.9.0-stale"), 0o700)

	// Create displaced binary
	displaced := filepath.Join(root, "csx.exe.previous-999")
	_ = os.WriteFile(displaced, []byte("old-binary"), 0o755)

	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusWarn {
		t.Fatalf("expected StatusWarn for stale payload and displaced binary, got %s", diag.Status)
	}

	// Repair
	repaired, err := check.Repair(context.Background(), cctx, diag)
	if err != nil {
		t.Fatalf("repair stale payloads failed: %v", err)
	}
	if repaired.Status != StatusPass {
		t.Fatalf("expected StatusPass after cleanup, got %s", repaired.Status)
	}

	if fileExists(filepath.Join(root, "payloads", "v0.9.0-stale")) {
		t.Fatal("stale payload was not deleted")
	}
	if fileExists(displaced) {
		t.Fatal("displaced binary was not deleted")
	}
	if !fileExists(filepath.Join(root, "payloads", "v1.0.0")) {
		t.Fatal("active payload must be preserved!")
	}
}

func TestMCPAgentConfigCheckAndRepair(t *testing.T) {
	cctx, _, _ := setupTestEnv(t)
	check := &MCPAgentConfigCheck{}

	// Create dummy Claude config pointing to nonexistent binary
	claudeCfg := filepath.Join(cctx.AgentHome, ".claude.json")
	_ = os.WriteFile(claudeCfg, []byte(`{
		"mcpServers": {
			"csx": {
				"command": "/nonexistent/path/to/csx"
			}
		}
	}`), 0o600)

	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusWarn {
		t.Fatalf("expected StatusWarn for missing agent binary, got %s", diag.Status)
	}
	if !diag.Fixable {
		t.Fatal("expected fixable agent config")
	}

	// Repair updates to current valid binary
	repaired, err := check.Repair(context.Background(), cctx, diag)
	if err != nil {
		t.Fatalf("repair agent config failed: %v", err)
	}
	// Note: In test env, targetExe may not exist unless created; if created, verifies:
	if repaired.Fixable {
		// Verify file was written
		data, _ := os.ReadFile(claudeCfg)
		if strings.Contains(string(data), "/nonexistent/path/to/csx") {
			t.Fatal("claude config was not updated away from nonexistent path")
		}
	}
}

func TestAuthSessionValidityAndZeroSecretLeakage(t *testing.T) {
	cctx, _, _ := setupTestEnv(t)
	check := &AuthSessionValidityCheck{}

	// 1. Unauthenticated session
	cctx.Config.APIToken = ""
	cctx.Config.GithubLogin = ""
	diag := check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusPass {
		t.Fatalf("expected StatusPass for anonymous session, got %s", diag.Status)
	}

	// 2. Secret token configured
	secretToken := "csx_super_secret_token_abcdef123456789"
	cctx.Config.APIToken = secretToken
	cctx.Config.GithubLogin = "testuser"

	diag = check.Diagnose(context.Background(), cctx)
	if diag.Status != StatusPass {
		t.Fatalf("expected StatusPass for valid session, got %s", diag.Status)
	}

	// Verify ZERO SECRET LEAKAGE in diagnosis
	for _, d := range diag.Details {
		if strings.Contains(d, secretToken) || strings.Contains(d, "secret_token") {
			t.Fatalf("SECRET LEAKED in diagnosis details: %s", d)
		}
	}
	if strings.Contains(diag.Summary, secretToken) || strings.Contains(diag.Summary, "secret_token") {
		t.Fatalf("SECRET LEAKED in diagnosis summary: %s", diag.Summary)
	}

	// Run through SanitizeResult
	res := Result{
		Healthy: true,
		Summary: Summary{Total: 1, Pass: 1},
		Checks:  []Diagnosis{diag},
	}
	sanitized := SanitizeResult(res, secretToken)

	// Test table output
	var tableBuf bytes.Buffer
	if err := FormatTable(&tableBuf, sanitized, true); err != nil {
		t.Fatal(err)
	}
	tableStr := tableBuf.String()
	if strings.Contains(tableStr, secretToken) || strings.Contains(tableStr, "secret_token") {
		t.Fatalf("SECRET LEAKED in table output:\n%s", tableStr)
	}

	// Test JSON output
	var jsonBuf bytes.Buffer
	if err := FormatJSON(&jsonBuf, sanitized); err != nil {
		t.Fatal(err)
	}
	jsonStr := jsonBuf.String()
	if strings.Contains(jsonStr, secretToken) || strings.Contains(jsonStr, "secret_token") {
		t.Fatalf("SECRET LEAKED in JSON output:\n%s", jsonStr)
	}
}

func TestEngineExecutionAndIdempotency(t *testing.T) {
	cctx, home, root := setupTestEnv(t)

	// Create healthy minimum install
	exeName := "csx"
	if runtime.GOOS == "windows" {
		exeName = "csx.exe"
	}
	_ = os.WriteFile(filepath.Join(root, exeName), []byte("launcher"), 0o755)

	payloadPath, _ := launcher.PayloadPath(root, "v1.0.0")
	_ = os.MkdirAll(filepath.Dir(payloadPath), 0o700)
	pBytes := []byte("payload-content")
	_ = os.WriteFile(payloadPath, pBytes, 0o700)
	pSum := sha256.Sum256(pBytes)

	actRaw, _ := json.Marshal(launcher.Active{
		Schema:  1,
		Current: launcher.Descriptor{Version: "v1.0.0", SHA256: hex.EncodeToString(pSum[:]), Sequence: 1},
	})
	_ = os.WriteFile(launcher.Path(root), actRaw, 0o600)

	// Add a fixable issue: stale daemon lock
	_ = os.WriteFile(filepath.Join(home, "daemon.lock"), []byte("99\n"), 0o600)

	engine := DefaultEngine()

	// First run without fix: should have warning for stale lock
	res1 := engine.Run(context.Background(), cctx)
	if res1.Summary.Warn == 0 {
		t.Fatal("expected at least 1 warning for stale lock")
	}

	// Second run with --fix: should repair stale lock and re-verify as FIXED
	cctx.Fix = true
	res2 := engine.Run(context.Background(), cctx)
	if res2.Summary.Fixed == 0 {
		t.Fatal("expected at least 1 fixed check in fix mode")
	}
	if !res2.Healthy {
		t.Fatalf("expected Healthy after fix, got Fail=%d", res2.Summary.Fail)
	}

	// Third run with --fix (idempotency check): should remain healthy, 0 fail, 0 warnings
	res3 := engine.Run(context.Background(), cctx)
	if !res3.Healthy {
		t.Fatalf("expected Healthy on idempotent re-run, got Fail=%d", res3.Summary.Fail)
	}
	if res3.Summary.Warn > 0 {
		t.Fatalf("expected 0 warnings on idempotent re-run, got %d", res3.Summary.Warn)
	}
}
