package purplepulse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrackOncePerLocalDay(t *testing.T) {
	var calls int
	var got []payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var p payload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("decode: %v", err)
		}
		got = append(got, p)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	home := t.TempDir()
	client := &http.Client{Timeout: time.Second}
	now := time.Date(2026, 9, 15, 23, 55, 0, 0, time.Local)

	clock := func() time.Time { return now }
	if err := track(home, "v0.1.200", "windows", "cli", "test", true, srv.URL, client, clock); err != nil {
		t.Fatalf("first track: %v", err)
	}
	if err := track(home, "v0.1.200", "windows", "cli", "test", true, srv.URL, client, clock); err != nil {
		t.Fatalf("second track: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if len(got) != 1 || got[0].ProjectID != projectID || got[0].Environment != "test" {
		t.Fatalf("payload = %+v", got)
	}
	if !validUUID(got[0].InstallID) {
		t.Fatalf("install_id = %q", got[0].InstallID)
	}
	firstID := got[0].InstallID

	now = now.Add(24 * time.Hour)
	if err := track(home, "v0.1.200", "windows", "cli", "test", true, srv.URL, client, clock); err != nil {
		t.Fatalf("next day track: %v", err)
	}
	if calls != 2 || got[1].InstallID != firstID {
		t.Fatalf("next day calls=%d ids=%q/%q", calls, firstID, got[1].InstallID)
	}
}

func TestDisabledNetworkStillPersistsInstallID(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	home := t.TempDir()
	now := func() time.Time { return time.Date(2026, 9, 15, 9, 0, 0, 0, time.Local) }
	if err := track(home, "v0.1.200", "windows", "cli", "", false, srv.URL, &http.Client{Timeout: time.Second}, now); err != nil {
		t.Fatalf("track disabled: %v", err)
	}
	if calls != 0 {
		t.Fatalf("disabled network made %d calls", calls)
	}
	raw, err := os.ReadFile(filepath.Join(home, stateFile))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var s state
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if !validUUID(s.InstallID) || s.LastAttempt != "" {
		t.Fatalf("state = %+v", s)
	}
}

func TestReleasePayloadOmitsEnvironment(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	home := t.TempDir()
	now := func() time.Time { return time.Date(2026, 9, 15, 8, 0, 0, 0, time.Local) }
	if err := track(home, "v0.1.200", "linux", "cli", "", true, srv.URL, &http.Client{Timeout: time.Second}, now); err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, ok := raw["environment"]; ok {
		t.Fatalf("production payload leaked environment: %#v", raw)
	}
	if raw["platform"] != "cli" || raw["os"] != "linux" {
		t.Fatalf("platform/os = %#v/%#v", raw["platform"], raw["os"])
	}
}

func TestFailedAttemptDoesNotRetrySameDay(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	home := t.TempDir()
	clock := func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.Local) }
	client := &http.Client{Timeout: time.Second}
	_ = track(home, "v0.1.200", "windows", "cli", "test", true, srv.URL, client, clock)
	_ = track(home, "v0.1.200", "windows", "cli", "test", true, srv.URL, client, clock)
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 after failed first attempt", calls)
	}
	raw, err := os.ReadFile(filepath.Join(home, stateFile))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var s state
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if s.LastAttempt != "2026-09-15" || !validUUID(s.InstallID) {
		t.Fatalf("state = %+v", s)
	}
}

func TestEnvironmentForVersion(t *testing.T) {
	if got := environmentForVersion("dev (git)"); got != "dev" {
		t.Fatalf("dev version environment = %q", got)
	}
	if got := environmentForVersion("v0.1.200"); got != "" {
		t.Fatalf("release version environment = %q", got)
	}
}

func TestOSMapping(t *testing.T) {
	if normalizeOS("darwin") != "macos" {
		t.Fatal("darwin must map to macos")
	}
}
