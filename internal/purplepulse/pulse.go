package purplepulse

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	projectID      = "pp_codesamplex_f2f2ab10"
	endpoint       = "https://pulse-api.purpleshiphub.workers.dev/api/v1/ping"
	stateFile      = "purplepulse.json"
	lockFilePrefix = ".purplepulse.lock."
	schemaVersion  = 2
	helperArg      = "__purplepulse_send"
	helperEnv      = "CSX_PURPLEPULSE_PAYLOAD"
)

var pulseClient = &http.Client{Timeout: 1500 * time.Millisecond}

type state struct {
	InstallID   string `json:"install_id"`
	LastAttempt string `json:"last_attempt,omitempty"`
}

type payload struct {
	ProjectID     string `json:"project_id"`
	InstallID     string `json:"install_id"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Platform      string `json:"platform"`
	SchemaVersion int    `json:"schema_version"`
	Environment   string `json:"environment,omitempty"`
}

// Track records the local daily attempt, then launches a detached helper so
// command completion never waits for telemetry and fast commands cannot kill it.
// Existing v1 state stays in purplepulse.json.
func Track(home, version, platform string, networkAllowed bool) {
	allowed := networkAllowed && !telemetryDisabled() && !ephemeralEnvironment()
	p, ok, err := prepare(home, version, normalizeOS(runtime.GOOS), platform,
		environmentForVersion(version), allowed, time.Now)
	if err != nil || !ok {
		return
	}
	_ = launchDetached(p)
}

func IsHelperInvocation(args []string) bool {
	return len(args) == 1 && args[0] == helperArg
}

// PlatformForArgs labels the long-lived MCP subprocess separately from normal CLI use.
func PlatformForArgs(args []string) string {
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "mcp") {
		return "mcp"
	}
	return "cli"
}

func RunHelperFromEnv() {
	p, ok := decodeHelperPayload(os.Getenv(helperEnv))
	if !ok {
		return
	}
	_ = send(endpoint, pulseClient, p)
}

func decodeHelperPayload(raw string) (payload, bool) {
	var p payload
	if raw == "" || json.Unmarshal([]byte(raw), &p) != nil {
		return payload{}, false
	}
	if p.ProjectID != projectID || p.SchemaVersion != schemaVersion || !validUUID(p.InstallID) {
		return payload{}, false
	}
	if p.Platform != "cli" && p.Platform != "mcp" {
		return payload{}, false
	}
	return p, true
}

func launchDetached(p payload) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, helperArg)
	cmd.Env = append(os.Environ(), helperEnv+"="+string(raw))
	configureDetached(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// track is the synchronous test seam used to verify send and de-duplication behavior.
func track(home, version, osName, platform, environment string, networkAllowed bool, target string, client *http.Client, now func() time.Time) error {
	p, ok, err := prepare(home, version, osName, platform, environment, networkAllowed, now)
	if err != nil || !ok {
		return err
	}
	return send(target, client, p)
}

func prepare(home, version, osName, platform, environment string, networkAllowed bool, now func() time.Time) (payload, bool, error) {
	if home == "" {
		return payload{}, false, nil
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return payload{}, false, err
	}

	today := now().UTC().Format("2006-01-02")
	lockPath := filepath.Join(home, lockFilePrefix+today)
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return payload{}, false, nil
	}
	_ = lock.Close()
	defer os.Remove(lockPath)

	statePath := filepath.Join(home, stateFile)
	s := readState(statePath)
	if !validUUID(s.InstallID) {
		id, err := newUUID()
		if err != nil {
			return payload{}, false, err
		}
		s.InstallID = id
		if err := writeState(statePath, s); err != nil {
			return payload{}, false, err
		}
	}
	if !networkAllowed || s.LastAttempt == today {
		return payload{}, false, nil
	}

	// Keep the v1 key/value shape and mark before network I/O to prevent retries.
	s.LastAttempt = today
	if err := writeState(statePath, s); err != nil {
		return payload{}, false, err
	}

	p := payload{
		ProjectID:     projectID,
		InstallID:     s.InstallID,
		Version:       version,
		OS:            osName,
		Platform:      platform,
		SchemaVersion: schemaVersion,
		Environment:   environment,
	}
	return p, true, nil
}

func send(target string, client *http.Client, p payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("purplepulse: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func telemetryDisabled() bool {
	return strings.TrimSpace(os.Getenv("DO_NOT_TRACK")) == "1" ||
		strings.TrimSpace(os.Getenv("CSX_TELEMETRY")) == "0"
}

func ephemeralEnvironment() bool {
	for _, key := range []string{
		"CI", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "TF_BUILD",
		"CIRCLECI", "JENKINS_URL", "TEAMCITY_VERSION", "CODEBUILD_BUILD_ID",
		"KUBERNETES_SERVICE_HOST", "ECS_CONTAINER_METADATA_URI", "ECS_CONTAINER_METADATA_URI_V4",
		"AWS_LAMBDA_FUNCTION_NAME", "FUNCTIONS_WORKER_RUNTIME", "K_SERVICE",
		"DOTNET_RUNNING_IN_CONTAINER", "RUNNING_IN_CONTAINER", "CONTAINER",
	} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat("/.dockerenv"); err == nil {
			return true
		}
	}
	return false
}

func readState(path string) state {
	var s state
	raw, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(raw, &s)
	return s
}

func writeState(path string, s state) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			_ = os.Remove(tmp)
			return err2
		}
	}
	return nil
}
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := make([]byte, 32)
	hex.Encode(h, b[:])
	return string(h[0:8]) + "-" + string(h[8:12]) + "-" + string(h[12:16]) + "-" + string(h[16:20]) + "-" + string(h[20:32]), nil
}

func validUUID(v string) bool {
	if len(v) != 36 || v[8] != '-' || v[13] != '-' || v[18] != '-' || v[23] != '-' {
		return false
	}
	for i, c := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

func normalizeOS(goos string) string {
	if goos == "darwin" {
		return "macos"
	}
	return goos
}

func environmentForVersion(version string) string {
	v := strings.ToLower(strings.TrimSpace(version))
	if v == "" || strings.Contains(v, "dev") || strings.Contains(v, "git") {
		return "dev"
	}
	return ""
}
