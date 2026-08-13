package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestGitHubDeviceFlowUnconfiguredIs501(t *testing.T) {
	srv, _, _ := newTestServer(t, nil) // no client id configured
	for _, path := range []string{"/v1/auth/github/device", "/v1/auth/github/poll"} {
		resp := postJSON(t, srv.URL+path, map[string]string{"deviceCode": "x"}, nil)
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s status = %d, want 501", path, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "github identity not configured") {
			t.Fatalf("%s body = %s", path, body)
		}
	}
}

// fakeGitHub serves the three device-flow endpoints. The first token poll
// returns authorization_pending; the second succeeds.
func fakeGitHub(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	polls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("client_id") != "client123" {
			http.Error(w, "bad client", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"device_code": "dev123", "user_code": "ABCD-1234",
			"verification_uri": "https://github.com/login/device", "interval": 5,
		})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("device_code") != "dev123" {
			writeJSON(w, http.StatusOK, map[string]string{"error": "incorrect_device_code"})
			return
		}
		polls++
		if polls == 1 {
			writeJSON(w, http.StatusOK, map[string]string{"error": "authorization_pending"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"access_token": "gho_testtoken"})
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_testtoken" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"login": "octocat", "id": 583231})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &polls
}

func TestGitHubDeviceFlowSuccess(t *testing.T) {
	gh, _ := fakeGitHub(t)
	srv, store, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.GithubClientID = "client123"
		d.Cfg.GithubClientSecret = "secret456"
		d.GitHubDeviceURL = gh.URL + "/device"
		d.GitHubTokenURL = gh.URL + "/token"
		d.GitHubUserURL = gh.URL + "/user"
	})

	// Step 1: device codes.
	var device struct {
		DeviceCode      string `json:"deviceCode"`
		UserCode        string `json:"userCode"`
		VerificationUri string `json:"verificationUri"`
		Interval        int    `json:"interval"`
	}
	resp := postJSON(t, srv.URL+"/v1/auth/github/device", map[string]string{}, &device)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("device status = %d", resp.StatusCode)
	}
	if device.DeviceCode != "dev123" || device.UserCode != "ABCD-1234" || device.Interval != 5 {
		t.Fatalf("device = %+v", device)
	}

	// Step 2: first poll is pending.
	var pending struct {
		Status string `json:"status"`
	}
	resp = postJSON(t, srv.URL+"/v1/auth/github/poll",
		map[string]string{"deviceCode": "dev123"}, &pending)
	if resp.StatusCode != http.StatusAccepted || pending.Status != "authorization_pending" {
		t.Fatalf("pending poll = %d %+v", resp.StatusCode, pending)
	}

	// Step 3: second poll succeeds; the api token is returned exactly once.
	var success struct {
		Login    string `json:"login"`
		APIToken string `json:"apiToken"`
	}
	resp = postJSON(t, srv.URL+"/v1/auth/github/poll",
		map[string]string{"deviceCode": "dev123"}, &success)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll status = %d", resp.StatusCode)
	}
	if success.Login != "octocat" || !strings.HasPrefix(success.APIToken, "csx_") {
		t.Fatalf("success = %+v", success)
	}

	// Only the hash is stored, and it resolves the identity.
	id, ok, err := store.IdentityByAPIToken(context.Background(), sha256Hex(success.APIToken))
	if err != nil || !ok || id.Login != "octocat" || id.GithubID != 583231 {
		t.Fatalf("identity = %+v ok=%v err=%v", id, ok, err)
	}
	if id.APITokenHash == success.APIToken {
		t.Fatal("raw api token must never be stored")
	}

	// The token attributes sample uploads to the seeder.
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, nil)
	up := postSample(t, srv.URL, manifest, domain.SHA256Hex(artifact), artifact, success.APIToken)
	if up.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(up.Body)
		t.Fatalf("upload with token = %d %s", up.StatusCode, body)
	}
	var meta struct {
		OriginSeeder string `json:"originSeeder"`
	}
	getJSON(t, srv.URL+"/v1/samples/"+domain.SHA256Hex(artifact), &meta)
	if meta.OriginSeeder != "octocat" {
		t.Fatalf("originSeeder = %q, want octocat", meta.OriginSeeder)
	}
}

func TestGitHubPollDeniedError(t *testing.T) {
	gh, _ := fakeGitHub(t)
	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.GithubClientID = "client123"
		d.GitHubDeviceURL = gh.URL + "/device"
		d.GitHubTokenURL = gh.URL + "/token"
		d.GitHubUserURL = gh.URL + "/user"
	})
	var out map[string]string
	resp := postJSON(t, srv.URL+"/v1/auth/github/poll",
		map[string]string{"deviceCode": "wrong"}, &out)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(out["error"], "incorrect_device_code") {
		t.Fatalf("error = %q", out["error"])
	}
}

// The mux itself: JSON error shape and panic guard.
func TestErrorShapeAndUnknownRoute(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	resp, err := http.Post(srv.URL+"/v1/search", "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || e.Error == "" {
		t.Fatalf("error body malformed: %v %+v", err, e)
	}

	resp, _ = http.Get(srv.URL + "/v1/nope")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route = %d, want 404", resp.StatusCode)
	}
}
