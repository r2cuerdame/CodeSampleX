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

func TestTrackOncePerUTCDay(t *testing.T) {
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
	kst := time.FixedZone("KST", 9*60*60)
	now := time.Date(2026, 9, 16, 0, 30, 0, 0, kst)
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
	if got[0].SchemaVersion != schemaVersion {
		t.Fatalf("schema_version = %d, want %d", got[0].SchemaVersion, schemaVersion)
	}
	firstID := got[0].InstallID
	if !validUUID(firstID) {
		t.Fatalf("install_id = %q", firstID)
	}

	// Local date is still Sep 16, but UTC has rolled from Sep 15 to Sep 16.
	now = now.Add(9 * time.Hour)
	if err := track(home, "v0.1.200", "windows", "cli", "test", true, srv.URL, client, clock); err != nil {
		t.Fatalf("next UTC day track: %v", err)
	}
	if calls != 2 || got[1].InstallID != firstID {
		t.Fatalf("next UTC day calls=%d ids=%q/%q", calls, firstID, got[1].InstallID)
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
	now := func() time.Time { return time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC) }
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

func TestReleasePayloadV2OmitsEnvironment(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	home := t.TempDir()
	now := func() time.Time { return time.Date(2026, 9, 16, 8, 0, 0, 0, time.UTC) }
	if err := track(home, "v0.1.200", "linux", "cli", "", true, srv.URL, &http.Client{Timeout: time.Second}, now); err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, ok := raw["environment"]; ok {
		t.Fatalf("production payload leaked environment: %#v", raw)
	}
	if raw["platform"] != "cli" || raw["os"] != "linux" {
		t.Fatalf("platform/os = %#v/%#v", raw["platform"], raw["os"])
	}
	if raw["schema_version"] != float64(2) {
		t.Fatalf("schema_version = %#v", raw["schema_version"])
	}
	if len(raw) != 6 {
		t.Fatalf("production payload has unexpected fields: %#v", raw)
	}
}

func TestFailedAttemptDoesNotRetrySameUTCDay(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	home := t.TempDir()
	clock := func() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) }
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
	if s.LastAttempt != "2026-09-16" || !validUUID(s.InstallID) {
		t.Fatalf("state = %+v", s)
	}
}

func TestV1StateIsReusedWithoutChangingSameDayValues(t *testing.T) {
	home := t.TempDir()
	statePath := filepath.Join(home, stateFile)
	original := []byte(`{"install_id":"37266039-77aa-4dbd-a3fb-7ca31984ff65","last_attempt":"2026-09-16"}`)
	if err := os.WriteFile(statePath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	now := func() time.Time { return time.Date(2026, 9, 16, 23, 59, 0, 0, time.UTC) }
	if err := track(home, "v0.1.200", "windows", "cli", "test", true, srv.URL, &http.Client{Timeout: time.Second}, now); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("existing v1 last_attempt should suppress same-day send, calls=%d", calls)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("v1 state changed during migration: got %s want %s", after, original)
	}
}

func TestOptOutAndEphemeralGuards(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "1")
	if !telemetryDisabled() {
		t.Fatal("DO_NOT_TRACK=1 must disable telemetry")
	}
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("CSX_TELEMETRY", "0")
	if !telemetryDisabled() {
		t.Fatal("CSX_TELEMETRY=0 must disable telemetry")
	}
	t.Setenv("CSX_TELEMETRY", "1")
	t.Setenv("CI", "1")
	if !ephemeralEnvironment() {
		t.Fatal("CI must be treated as ephemeral")
	}
}

func TestHelperPayloadValidation(t *testing.T) {
	p := payload{ProjectID: projectID, InstallID: "37266039-77aa-4dbd-a3fb-7ca31984ff65", Version: "v0.1.193", OS: "windows", Platform: "mcp", SchemaVersion: 2}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decodeHelperPayload(string(raw))
	if !ok || got.InstallID != p.InstallID || got.Platform != "mcp" {
		t.Fatalf("decoded = %+v ok=%v", got, ok)
	}
	p.SchemaVersion = 1
	raw, _ = json.Marshal(p)
	if _, ok := decodeHelperPayload(string(raw)); ok {
		t.Fatal("v1 helper payload must be rejected")
	}
	if _, ok := decodeHelperPayload(`{"project_id":"wrong"}`); ok {
		t.Fatal("invalid helper payload accepted")
	}
}

func TestHelperInvocation(t *testing.T) {
	if !IsHelperInvocation([]string{helperArg}) {
		t.Fatal("helper arg not recognized")
	}
	if IsHelperInvocation([]string{"search"}) {
		t.Fatal("normal command recognized as helper")
	}
}

func TestPlatformForArgs(t *testing.T) {
	if got := PlatformForArgs([]string{"mcp"}); got != "mcp" {
		t.Fatalf("mcp platform = %q", got)
	}
	if got := PlatformForArgs([]string{"search", "axios"}); got != "cli" {
		t.Fatalf("cli platform = %q", got)
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
