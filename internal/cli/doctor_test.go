package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/launcher"
	"github.com/r2cuerdame/codesamplex/internal/update"
)

type doctorTransport func(*http.Request) (*http.Response, error)

func (f doctorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func doctorDigest(s string) string                                          { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func doctorFixture(t *testing.T) (string, string, *bytes.Buffer) {
	t.Helper()
	for _, env := range []string{"CSX_LAUNCHER_ROOT", "CSX_LAUNCHER_PATH", "CSX_LAUNCHER_VERSION"} {
		t.Setenv(env, "")
	}
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	t.Setenv("CSX_AGENT_HOME", home)
	exe := filepath.Join(home, "csx-test.exe")
	if err := os.WriteFile(exe, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.EnsureHome(home); err != nil {
		t.Fatal(err)
	}
	if err := update.AdoptStandalone(home, exe); err != nil {
		t.Fatal(err)
	}
	output := new(bytes.Buffer)
	oldHome, oldExe, oldOutput, oldMCP, oldVersion, oldHTTP, oldRepair, oldRepairBinding, oldOwned, oldProbe, oldProcesses, oldLauncherProbe := doctorHome, doctorExecutable, doctorOutput, mcpCommand, Version, doctorHTTP, doctorRehydrate, doctorRepairReleaseBinding, doctorOwnedLauncher, doctorMCPProbe, doctorMCPProcesses, doctorLauncherVersionProbe
	doctorHome = func() (string, error) { return home, nil }
	doctorExecutable = func() (string, error) { return exe, nil }
	doctorOutput = output
	mcpCommand = func() string { return exe }
	doctorMCPProbe = func(context.Context, string) error { return nil }
	doctorMCPProcesses = func(context.Context, string) (bool, bool) { return false, true }
	doctorLauncherVersionProbe = func(context.Context, string, string) error { return nil }
	Version = "dev (git)"
	doctorHTTP = &http.Client{Timeout: time.Second}
	t.Cleanup(func() {
		doctorHome, doctorExecutable, doctorOutput, mcpCommand, Version, doctorHTTP, doctorRehydrate, doctorRepairReleaseBinding, doctorOwnedLauncher, doctorMCPProbe, doctorMCPProcesses, doctorLauncherVersionProbe = oldHome, oldExe, oldOutput, oldMCP, oldVersion, oldHTTP, oldRepair, oldRepairBinding, oldOwned, oldProbe, oldProcesses, oldLauncherProbe
	})
	return home, exe, output
}

func TestDoctorRepairsOnlyOwnedCodexBlock(t *testing.T) {
	home, _, out := doctorFixture(t)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "userSetting = \"keep me\"\n\n" + tomlBegin + "\n[mcp_servers.csx]\ncommand = \"missing-csx\"\nargs = [\"mcp\"]\n" + tomlEnd + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := doctorMain(context.Background(), []string{"--json"}); code != 1 {
		t.Fatalf("stale config code=%d", code)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != original {
		t.Fatal("diagnosis modified user config")
	}
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 0 {
		t.Fatalf("repair code=%d: %s", code, out.String())
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "userSetting = \"keep me\"") || !strings.Contains(string(raw), strings.TrimSpace(codexMCPBlock())) {
		t.Fatal("repair did not preserve user setting and regenerate owned block")
	}
}

func TestDoctorReclaimsOnlyStaleUpdateLock(t *testing.T) {
	home, _, out := doctorFixture(t)
	path := filepath.Join(home, "update", "update.lock")
	if err := os.WriteFile(path, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2147483647\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !update.UpdateLockStale(home) {
		t.Skip("test PID appears live on this host")
	}
	if code := doctorMain(context.Background(), []string{"--json"}); code != 1 {
		t.Fatalf("stale lock code=%d", code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("read-only diagnosis removed lock")
	}
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 0 {
		t.Fatalf("lock repair code=%d: %s", code, out.String())
	}
	if update.UpdateLockStale(home) {
		t.Fatal("stale lock persisted")
	}
	if !strings.Contains(out.String(), "FIXED") {
		t.Fatal("lock was not re-verified")
	}
}

func TestDoctorCleansOnlyOldUncommittedCacheTemp(t *testing.T) {
	home, _, out := doctorFixture(t)
	old := filepath.Join(home, "cas", "tmp-abandoned")
	fresh := filepath.Join(home, "cas", "tmp-active")
	object := filepath.Join(home, "cas", "sha256-object")
	for _, path := range []string{old, fresh, object} {
		if err := os.WriteFile(path, []byte("keep or clean"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	then := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, then, then); err != nil {
		t.Fatal(err)
	}
	if code := doctorMain(context.Background(), []string{"--json"}); code != 0 {
		t.Fatalf("stale cache warning code=%d", code)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("diagnosis removed old temp")
	}
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 0 || !strings.Contains(out.String(), "FIXED") {
		t.Fatalf("cache repair failed: %d %s", code, out.String())
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old uncommitted temp survived")
	}
	for _, path := range []string{fresh, object} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("nonstale cache entry removed")
		}
	}
}

func TestDoctorSignatureMismatchFailsClosed(t *testing.T) {
	_, _, out := doctorFixture(t)
	Version = "v1.2.3"
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	old := update.PublicKeyBase64
	update.PublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { update.PublicKeyBase64 = old })
	doctorHTTP = &http.Client{Transport: doctorTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"payload":"dGFtcGVyZWQ=","signature":"YmFk"}`)), Header: make(http.Header)}, nil
	})}
	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 1 {
		t.Fatalf("signature mismatch code=%d", code)
	}
	if !strings.Contains(out.String(), "manifest-unverified") || strings.Contains(out.String(), "tampered") {
		t.Fatal("signature failure not fail-closed and scrubbed")
	}
}

func TestDoctorReleaseNetworkRetryAndStaleMCPProcess(t *testing.T) {
	_, _, out := doctorFixture(t)
	Version = "v1.2.3"
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	old := update.PublicKeyBase64
	update.PublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { update.PublicKeyBase64 = old })
	doctorMCPProcesses = func(context.Context, string) (bool, bool) { return true, true }
	doctorHTTP = &http.Client{Transport: doctorTransport(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })}
	if code := doctorMain(context.Background(), []string{"--json"}); code != 1 {
		t.Fatalf("unverified release code=%d", code)
	}
	if !strings.Contains(out.String(), "release-unreachable") || !strings.Contains(out.String(), "stale-mcp-process") || strings.Contains(out.String(), "unexpected EOF") {
		t.Fatal("retryable release or stale MCP diagnosis incorrect")
	}
}

func TestDoctorMCPProbeIntegration(t *testing.T) {
	exe := os.Getenv("CSX_DOCTOR_TEST_BINARY")
	if exe == "" {
		t.Skip("set CSX_DOCTOR_TEST_BINARY to exercise the built CLI")
	}
	if err := probeMCPStartup(context.Background(), exe); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorLauncherVersionProbeIntegration(t *testing.T) {
	exe := os.Getenv("CSX_DOCTOR_TEST_LAUNCHER")
	if exe == "" {
		t.Skip("set CSX_DOCTOR_TEST_LAUNCHER to exercise a built launcher")
	}
	if err := probeLauncherVersion(context.Background(), exe, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorReadOnlyFixAndIdempotence(t *testing.T) {
	home, _, out := doctorFixture(t)
	if err := os.Remove(filepath.Join(home, "cas")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(update.InstallPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if code := doctorMain(context.Background(), []string{"--json"}); code != 1 {
		t.Fatalf("read-only code=%d", code)
	}
	if _, err := os.Stat(filepath.Join(home, "cas")); !os.IsNotExist(err) {
		t.Fatal("read-only diagnosis created cache directory")
	}
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 0 {
		t.Fatalf("fix code=%d: %s", code, out.String())
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.Health != "HEALTHY" {
		t.Fatalf("report=%+v", report)
	}
	fixed := false
	for _, c := range report.Checks {
		if c.ID == "cache-cas" && c.Status == "FIXED" {
			fixed = true
		}
	}
	if !fixed {
		t.Fatal("repaired cache was not re-verified")
	}
	first, err := os.Stat(filepath.Join(home, "cas"))
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 0 {
		t.Fatalf("repeat code=%d: %s", code, out.String())
	}
	second, _ := os.Stat(filepath.Join(home, "cas"))
	if !first.ModTime().Equal(second.ModTime()) {
		t.Fatal("healthy cache modified on repeated fix")
	}
	after, _ := os.ReadFile(update.InstallPath(home))
	if !bytes.Equal(before, after) {
		t.Fatal("install marker changed")
	}
}

func TestDoctorDispatcherNeverStampsActivation(t *testing.T) {
	home, _, _ := doctorFixture(t)
	for _, args := range [][]string{{"doctor", "--json"}, {"--debug", "doctor", "--json"}} {
		if code := Main(args); code != 0 {
			t.Fatalf("doctor dispatch code=%d", code)
		}
		if _, err := os.Stat(filepath.Join(home, "csx.db")); !os.IsNotExist(err) {
			t.Fatal("read-only doctor stamped activation")
		}
	}
}

func TestDoctorInvalidAuthAndJSONNeverLeakSecrets(t *testing.T) {
	home, _, out := doctorFixture(t)
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	cfg.GithubLogin = "someone"
	cfg.APIToken = "PRIVATE_TOKEN_123"
	cfg.ServerURL = "https://secret.example/private"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	if code := doctorMain(context.Background(), []string{"--json", "--verbose"}); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(out.String(), cfg.APIToken) || strings.Contains(out.String(), "secret.example") || strings.Contains(out.String(), home) {
		t.Fatal("private value leaked")
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.Health != "UNHEALTHY" || len(report.Checks) == 0 {
		t.Fatal("unstable JSON contract")
	}
	found := false
	for _, c := range report.Checks {
		if c.ID == "auth" && c.Code == "token-format-invalid" {
			found = true
		}
	}
	if !found {
		t.Fatal("invalid token was not diagnosed")
	}
	cfg.APIToken = "csx_" + strings.Repeat("a", 48)
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--json"}); code != 0 || !strings.Contains(out.String(), "session-not-verifiable") || strings.Contains(out.String(), cfg.APIToken) {
		t.Fatal("well-formed token was not handled without disclosure")
	}
}

func TestDoctorSeederTokenAuthGivesWarnWithoutLeak(t *testing.T) {
	home, _, out := doctorFixture(t)
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	seederToken := "csx_" + strings.Repeat("0123456789abcdef", 4) // 4 + 64 = 68 chars
	cfg.APIToken = seederToken
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}

	// 1. JSON and verbose output
	if code := doctorMain(context.Background(), []string{"--json", "--verbose"}); code != 0 {
		t.Fatalf("expected code 0 for seeder token, got code=%d output=%s", code, out.String())
	}
	outputStr := out.String()
	if strings.Contains(outputStr, seederToken) || strings.Contains(outputStr, seederToken[4:]) {
		t.Fatal("seeder token bytes leaked in JSON/verbose output")
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Health != "HEALTHY" {
		t.Fatalf("expected HEALTHY, got %s", report.Health)
	}
	found := false
	for _, c := range report.Checks {
		if c.ID == "auth" {
			found = true
			if c.Status != "WARN" || c.Code != "session-not-verifiable" {
				t.Fatalf("auth check expected WARN session-not-verifiable, got status=%s code=%s", c.Status, c.Code)
			}
		}
	}
	if !found {
		t.Fatal("auth check not found in doctor report")
	}

	// 2. Human-readable output without flags
	out.Reset()
	if code := doctorMain(context.Background(), []string{}); code != 0 {
		t.Fatalf("expected code 0 for human output, got code=%d output=%s", code, out.String())
	}
	humanOut := out.String()
	if strings.Contains(humanOut, seederToken) || strings.Contains(humanOut, seederToken[4:]) {
		t.Fatal("seeder token bytes leaked in human output")
	}
	if !strings.Contains(humanOut, "WARN auth") || !strings.Contains(humanOut, "HEALTHY") {
		t.Fatalf("expected WARN auth and HEALTHY in human output, got: %s", humanOut)
	}
}

func TestDoctorNetworkFailureIsRetryableAndScrubbed(t *testing.T) {
	home, _, out := doctorFixture(t)
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = "https://private.example/secret"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	doctorHTTP = &http.Client{Transport: doctorTransport(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })}
	if code := doctorMain(context.Background(), []string{"--json"}); code != 0 {
		t.Fatalf("transient server outage made local install unhealthy: %s", out.String())
	}
	if !strings.Contains(out.String(), "server-unreachable") || strings.Contains(out.String(), "private.example") {
		t.Fatal("network diagnosis incorrect or leaked URL")
	}
}

func TestDoctorServerAPIAndRegistryReachability(t *testing.T) {
	home, _, out := doctorFixture(t)
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = "https://codesamplex.example"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	doctorHTTP = &http.Client{Transport: doctorTransport(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		if r.URL.Path == "/version" {
			body = `{"service":"csx-server","version":"v1.0.0"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if code := doctorMain(context.Background(), []string{"--json"}); code != 0 {
		t.Fatalf("server and registries code=%d: %s", code, out.String())
	}
	for _, want := range []string{"server-api-ready", "registry-reachable"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
}

func TestDoctorRejectsIncompatibleServerRoute(t *testing.T) {
	home, _, out := doctorFixture(t)
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = "https://codesamplex.example"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	doctorHTTP = &http.Client{Transport: doctorTransport(func(r *http.Request) (*http.Response, error) {
		status, body := 200, "{}"
		if r.URL.Path == "/version" {
			body = `{"service":"csx-server","version":"v1.0.0"}`
		} else if r.URL.Path == "/v1/adapters" {
			status = 404
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if code := doctorMain(context.Background(), []string{"--json"}); code != 1 || !strings.Contains(out.String(), "server-api-incompatible") {
		t.Fatalf("incompatible server accepted: %d %s", code, out.String())
	}
}

func doctorSign(t *testing.T, m update.Manifest, key ed25519.PrivateKey) []byte {
	t.Helper()
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(update.Envelope{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func doctorSignedFixture(t *testing.T, version, digest string, sequence uint64) {
	doctorSignedFixtureWithBytes(t, version, digest, []byte("signed payload"), sequence)
}

func doctorSignedFixtureWithBytes(t *testing.T, version, digest string, payloadContent []byte, sequence uint64) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	update.PublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { update.PublicKeyBase64 = "" })
	assets := []update.Asset{}
	for _, target := range [][2]string{{"windows", "amd64"}, {"windows", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}} {
		name := "csx-" + target[0] + "-" + target[1]
		if target[0] == "windows" {
			name += ".exe"
		}
		hash := strings.Repeat("a", 64)
		size := int64(16)
		if target[0] == runtime.GOOS && target[1] == runtime.GOARCH {
			hash = digest
			if payloadContent != nil {
				size = int64(len(payloadContent))
			}
		}
		assets = append(assets, update.Asset{OS: target[0], Arch: target[1], URL: update.DefaultReleaseDownloadBase + "/" + version + "/" + name, Size: size, SHA256: hash})
	}
	m := update.Manifest{Schema: 1, Channel: "stable", Sequence: sequence, Version: version, PublishedAt: time.Now().Add(-time.Hour).UTC().Truncate(time.Second), ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second), Assets: assets}
	b := m
	b.Assets = append([]update.Asset(nil), m.Assets...)
	for i := range b.Assets {
		if b.Assets[i].OS == "windows" {
			b.Assets[i].LauncherURL = update.DefaultReleaseDownloadBase + "/" + version + "/csx-launcher-windows-" + b.Assets[i].Arch + ".exe"
			b.Assets[i].LauncherSize = 8
			b.Assets[i].LauncherSHA256 = doctorDigest("launcher")
		}
	}
	stable, bootstrap := doctorSign(t, m, key), doctorSign(t, b, key)
	doctorHTTP = &http.Client{Transport: doctorTransport(func(r *http.Request) (*http.Response, error) {
		body := stable
		if strings.HasSuffix(r.URL.Path, "csx-bootstrap-stable.json") {
			body = bootstrap
		} else if strings.HasSuffix(r.URL.Path, "csx-update-stable.json") {
			body = stable
		} else if payloadContent != nil {
			body = payloadContent
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
}

func TestDoctorCorruptPayloadRepairAndFailedRepair(t *testing.T) {
	home, _, out := doctorFixture(t)
	Version = "v1.2.3"
	root := filepath.Join(home, "install")
	path, err := launcher.PayloadPath(root, Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	good := "signed payload"
	d := launcher.Descriptor{Version: Version, SHA256: doctorDigest(good), Sequence: 10}
	pointer, _ := json.Marshal(launcher.Active{Schema: 1, Current: d})
	if err := os.WriteFile(launcher.Path(root), pointer, 0o600); err != nil {
		t.Fatal(err)
	}
	launcherPath := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(launcherPath, []byte("launcher"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := update.Install{Schema: 1, Kind: "launcher", ExecutablePath: path, InstallRoot: root, LauncherPath: launcherPath}
	raw, _ := json.Marshal(marker)
	if err := os.WriteFile(update.InstallPath(home), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	doctorExecutable = func() (string, error) { return path, nil }
	mcpCommand = func() string { return launcherPath }
	doctorOwnedLauncher = func(update.Install) bool { return true }
	doctorSignedFixture(t, Version, d.SHA256, d.Sequence)
	if code := doctorMain(context.Background(), []string{"--json"}); code != 1 {
		t.Fatalf("corrupt payload code=%d", code)
	}
	disk, _ := os.ReadFile(path)
	if string(disk) != "corrupt" {
		t.Fatal("diagnosis modified payload")
	}
	doctorRehydrate = func(context.Context, string, update.RehydrateOptions) (update.RehydrateReport, error) {
		return update.RehydrateReport{}, io.ErrUnexpectedEOF
	}
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 1 || !strings.Contains(out.String(), "UNHEALTHY") {
		t.Fatalf("failed repair falsely healthy: %d %s", code, out.String())
	}
	doctorRehydrate = func(_ context.Context, _ string, _ update.RehydrateOptions) (update.RehydrateReport, error) {
		return update.RehydrateReport{}, os.WriteFile(path, []byte(good), 0o700)
	}
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 0 {
		t.Fatalf("repair code=%d %s", code, out.String())
	}
	if !strings.Contains(out.String(), "FIXED") {
		t.Fatal("repair not re-verified")
	}
	Version = "v1.2.4"
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--json"}); code != 1 || !strings.Contains(out.String(), "launcher-payload-version") {
		t.Fatalf("stale pairing was not diagnosed: %d %s", code, out.String())
	}
}

func TestDoctorReadOnlyHomeByteIdentical(t *testing.T) {
	for _, env := range []string{"CSX_LAUNCHER_ROOT", "CSX_LAUNCHER_PATH", "CSX_LAUNCHER_VERSION"} {
		t.Setenv(env, "")
	}
	// 1. Empty home test
	emptyHome := t.TempDir()
	doctorHome = func() (string, error) { return emptyHome, nil }
	doctorExecutable = func() (string, error) { return filepath.Join(emptyHome, "csx.exe"), nil }
	doctorOutput = new(bytes.Buffer)
	_ = doctorMain(context.Background(), []string{"--json"})
	entries, err := os.ReadDir(emptyHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("read-only doctor mutated empty CSX_HOME: %v", names)
	}

	// 2. Populated home test
	home, _, _ := doctorFixture(t)
	type fileRecord struct {
		isDir bool
		hash  string
	}
	snapshot := func() map[string]fileRecord {
		m := make(map[string]fileRecord)
		_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(home, path)
			if info.IsDir() {
				m[rel] = fileRecord{isDir: true}
				return nil
			}
			raw, _ := os.ReadFile(path)
			h := sha256.Sum256(raw)
			m[rel] = fileRecord{isDir: false, hash: hex.EncodeToString(h[:])}
			return nil
		})
		return m
	}

	before := snapshot()
	for _, args := range [][]string{{"--json"}, {"--verbose"}, {}} {
		out := new(bytes.Buffer)
		doctorOutput = out
		_ = doctorMain(context.Background(), args)
		after := snapshot()
		if len(before) != len(after) {
			t.Fatalf("read-only doctor changed file count: before=%d after=%d", len(before), len(after))
		}
		for path, rec := range before {
			afterRec, ok := after[path]
			if !ok {
				t.Fatalf("file disappeared after read-only doctor: %s", path)
			}
			if rec.isDir != afterRec.isDir || rec.hash != afterRec.hash {
				t.Fatalf("file content changed after read-only doctor: %s", path)
			}
		}
	}
}

func TestDoctorReleaseBindingRepairAndReverification(t *testing.T) {
	home, _, out := doctorFixture(t)
	Version = "v1.2.3"
	t.Setenv("LOCALAPPDATA", home)
	root := filepath.Join(home, "csx")
	goodPayload := "valid signed binary"
	goodHash := doctorDigest(goodPayload)
	goodSequence := uint64(15)

	payloadDir := filepath.Join(root, "payloads", Version)
	if err := os.MkdirAll(payloadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(payloadDir, "csx-payload.exe")
	if err := os.WriteFile(payloadPath, []byte(goodPayload), 0o700); err != nil {
		t.Fatal(err)
	}
	launcherPath := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(launcherPath, []byte("launcher"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Active pointer records mismatched SHA256 and sequence (the headline release-binding failure)
	mismatchedHash := strings.Repeat("b", 64)
	d := launcher.Descriptor{Version: Version, SHA256: mismatchedHash, Sequence: 10}
	pointer, _ := json.Marshal(launcher.Active{Schema: 1, Current: d})
	if err := os.WriteFile(launcher.Path(root), pointer, 0o600); err != nil {
		t.Fatal(err)
	}
	marker := update.Install{Schema: 1, Kind: "launcher", ExecutablePath: payloadPath, InstallRoot: root, LauncherPath: launcherPath}
	raw, _ := json.Marshal(marker)
	if err := os.WriteFile(update.InstallPath(home), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	doctorExecutable = func() (string, error) { return payloadPath, nil }
	mcpCommand = func() string { return launcherPath }
	doctorOwnedLauncher = func(update.Install) bool { return true }
	doctorSignedFixtureWithBytes(t, Version, goodHash, []byte(goodPayload), goodSequence)

	// Diagnosis without fix reports release-binding FAIL and points to --fix
	if code := doctorMain(context.Background(), []string{"--json"}); code != 1 {
		t.Fatalf("release binding mismatch code=%d", code)
	}
	if !strings.Contains(out.String(), "release-binding") || !strings.Contains(out.String(), "csx doctor --fix") {
		t.Fatalf("diagnosis did not report release-binding or action: %s", out.String())
	}

	// --fix repairs the release binding, reconciles active.json, and re-verifies
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 0 {
		t.Fatalf("repair code=%d %s", code, out.String())
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Health != "HEALTHY" {
		t.Fatalf("expected HEALTHY after fix, got %s", report.Health)
	}
	fixed := false
	for _, c := range report.Checks {
		if c.ID == "stable-manifest" && c.Status == "FIXED" {
			fixed = true
		}
	}
	if !fixed {
		t.Fatalf("stable-manifest was not marked FIXED: %s", out.String())
	}
	// Verify active.json was reconciled to the signed descriptor
	active, err := launcher.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if active.Current.SHA256 != goodHash || active.Current.Sequence != goodSequence {
		t.Fatalf("active.json was not reconciled: got %+v", active.Current)
	}
}

func TestDoctorLauncherPayloadPairingRepairAndReverification(t *testing.T) {
	home, _, out := doctorFixture(t)
	Version = "v1.2.3"
	t.Setenv("LOCALAPPDATA", home)
	root := filepath.Join(home, "csx")
	goodPayload := "valid signed binary"
	goodHash := doctorDigest(goodPayload)
	goodSequence := uint64(20)

	payloadDir := filepath.Join(root, "payloads", Version)
	if err := os.MkdirAll(payloadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(payloadDir, "csx-payload.exe")
	if err := os.WriteFile(payloadPath, []byte(goodPayload), 0o700); err != nil {
		t.Fatal(err)
	}
	launcherPath := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(launcherPath, []byte("launcher"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Old payload exists and verifies for older version v1.2.0
	oldPayloadDir := filepath.Join(root, "payloads", "v1.2.0")
	if err := os.MkdirAll(oldPayloadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPayload := "older binary"
	oldHash := doctorDigest(oldPayload)
	if err := os.WriteFile(filepath.Join(oldPayloadDir, "csx-payload.exe"), []byte(oldPayload), 0o700); err != nil {
		t.Fatal(err)
	}
	oldDescriptor := launcher.Descriptor{Version: "v1.2.0", SHA256: oldHash, Sequence: 10}
	pointer, _ := json.Marshal(launcher.Active{Schema: 1, Current: oldDescriptor})
	if err := os.WriteFile(launcher.Path(root), pointer, 0o600); err != nil {
		t.Fatal(err)
	}
	marker := update.Install{Schema: 1, Kind: "launcher", ExecutablePath: payloadPath, InstallRoot: root, LauncherPath: launcherPath}
	raw, _ := json.Marshal(marker)
	if err := os.WriteFile(update.InstallPath(home), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	doctorExecutable = func() (string, error) { return payloadPath, nil }
	mcpCommand = func() string { return launcherPath }
	doctorOwnedLauncher = func(update.Install) bool { return true }
	doctorSignedFixtureWithBytes(t, Version, goodHash, []byte(goodPayload), goodSequence)

	// Diagnosis without fix reports launcher-payload-version FAIL and launcher-payload-pair FAIL
	if code := doctorMain(context.Background(), []string{"--json"}); code != 1 {
		t.Fatalf("pairing mismatch code=%d", code)
	}
	if !strings.Contains(out.String(), "launcher-payload-version") || !strings.Contains(out.String(), "launcher-payload-pair") {
		t.Fatalf("diagnosis did not report pairing mismatch: %s", out.String())
	}

	// --fix reconciles active.json and re-verifies
	out.Reset()
	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 0 {
		t.Fatalf("pairing repair code=%d %s", code, out.String())
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Health != "HEALTHY" {
		t.Fatalf("expected HEALTHY after pairing fix, got %s", report.Health)
	}
	active, err := launcher.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if active.Current.Version != Version || active.Current.SHA256 != goodHash {
		t.Fatalf("pairing was not reconciled: got %+v", active.Current)
	}
}

func TestDoctorReleaseBindingSignatureFailureKeepsOfficialInstallerFallback(t *testing.T) {
	home, _, out := doctorFixture(t)
	Version = "v1.2.3"
	root := filepath.Join(home, "install")
	payloadPath, _ := launcher.PayloadPath(root, Version)
	_ = os.MkdirAll(filepath.Dir(payloadPath), 0o700)
	_ = os.WriteFile(payloadPath, []byte("payload"), 0o700)
	launcherPath := filepath.Join(root, "csx.exe")
	_ = os.WriteFile(launcherPath, []byte("launcher"), 0o700)
	d := launcher.Descriptor{Version: Version, SHA256: strings.Repeat("b", 64), Sequence: 10}
	pointer, _ := json.Marshal(launcher.Active{Schema: 1, Current: d})
	_ = os.WriteFile(launcher.Path(root), pointer, 0o600)
	marker := update.Install{Schema: 1, Kind: "launcher", ExecutablePath: payloadPath, InstallRoot: root, LauncherPath: launcherPath}
	raw, _ := json.Marshal(marker)
	_ = os.WriteFile(update.InstallPath(home), raw, 0o600)
	doctorExecutable = func() (string, error) { return payloadPath, nil }
	mcpCommand = func() string { return launcherPath }
	doctorOwnedLauncher = func(update.Install) bool { return true }

	// Bad signature on manifest:
	pub, _, _ := ed25519.GenerateKey(nil)
	old := update.PublicKeyBase64
	update.PublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { update.PublicKeyBase64 = old })
	doctorHTTP = &http.Client{Transport: doctorTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"payload":"dGFtcGVyZWQ=","signature":"YmFk"}`)), Header: make(http.Header)}, nil
	})}

	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 1 {
		t.Fatalf("signature failure allowed repair: %d %s", code, out.String())
	}
	if !strings.Contains(out.String(), "manifest-unverified") || !strings.Contains(out.String(), "never bypass signature verification") {
		t.Fatalf("expected fail-closed signature check: %s", out.String())
	}
	// Verify active.json was not mutated
	active, _ := launcher.Read(root)
	if active.Current.SHA256 != d.SHA256 {
		t.Fatal("active.json was modified despite signature failure")
	}
}

func TestDoctorAmbiguousStateKeepsOfficialInstallerFallback(t *testing.T) {
	home, _, out := doctorFixture(t)
	Version = "v1.2.3"
	root := filepath.Join(home, "install")
	payloadPath, _ := launcher.PayloadPath(root, Version)
	_ = os.MkdirAll(filepath.Dir(payloadPath), 0o700)
	_ = os.WriteFile(payloadPath, []byte("payload"), 0o700)
	launcherPath := filepath.Join(root, "csx.exe")
	_ = os.WriteFile(launcherPath, []byte("launcher"), 0o700)
	d := launcher.Descriptor{Version: Version, SHA256: strings.Repeat("b", 64), Sequence: 10}
	pointer, _ := json.Marshal(launcher.Active{Schema: 1, Current: d})
	_ = os.WriteFile(launcher.Path(root), pointer, 0o600)
	marker := update.Install{Schema: 1, Kind: "launcher", ExecutablePath: payloadPath, InstallRoot: root, LauncherPath: launcherPath}
	raw, _ := json.Marshal(marker)
	_ = os.WriteFile(update.InstallPath(home), raw, 0o600)
	doctorExecutable = func() (string, error) { return payloadPath, nil }
	mcpCommand = func() string { return launcherPath }
	// Outside CSX owned path or untrusted
	doctorOwnedLauncher = func(update.Install) bool { return false }
	doctorSignedFixture(t, Version, doctorDigest("valid"), 20)

	if code := doctorMain(context.Background(), []string{"--fix", "--json"}); code != 1 {
		t.Fatalf("ambiguous state allowed repair: %d %s", code, out.String())
	}
	active, _ := launcher.Read(root)
	if active.Current.SHA256 != d.SHA256 {
		t.Fatal("active.json was modified in ambiguous state")
	}
}
