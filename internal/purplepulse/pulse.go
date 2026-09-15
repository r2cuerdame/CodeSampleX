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
)

var pulseClient = &http.Client{Timeout: 500 * time.Millisecond}

type state struct {
	InstallID   string `json:"install_id"`
	LastAttempt string `json:"last_attempt,omitempty"`
}

type payload struct {
	ProjectID   string `json:"project_id"`
	InstallID   string `json:"install_id"`
	Version     string `json:"version"`
	OS          string `json:"os"`
	Platform    string `json:"platform"`
	Environment string `json:"environment,omitempty"`
}

// TrackCLI persists a PurplePulse install ID on first execution. Network
// activity is allowed only when the caller has already confirmed community
// mode and a public client class. Errors are deliberately fail-open.
func TrackCLI(home, version string, networkAllowed bool) {
	_ = track(home, version, normalizeOS(runtime.GOOS), "cli", environmentForVersion(version), networkAllowed, endpoint, pulseClient, time.Now)
}

func track(home, version, osName, platform, environment string, networkAllowed bool, target string, client *http.Client, now func() time.Time) error {
	if home == "" {
		return nil
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}

	today := now().Format("2006-01-02")
	lockPath := filepath.Join(home, lockFilePrefix+today)
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil
	}
	_ = lock.Close()
	defer os.Remove(lockPath)

	statePath := filepath.Join(home, stateFile)
	s := readState(statePath)
	if !validUUID(s.InstallID) {
		id, err := newUUID()
		if err != nil {
			return err
		}
		s.InstallID = id
		if err := writeState(statePath, s); err != nil {
			return err
		}
	}
	if !networkAllowed || s.LastAttempt == today {
		return nil
	}

	// Mark before the network call: a failure must not create a retry storm.
	s.LastAttempt = today
	if err := writeState(statePath, s); err != nil {
		return err
	}

	p := payload{
		ProjectID: projectID, InstallID: s.InstallID, Version: version,
		OS: osName, Platform: platform, Environment: environment,
	}
	return send(target, client, p)
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
