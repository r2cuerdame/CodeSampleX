package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// handleGitHubDevice implements POST /v1/auth/github/device: start the
// GitHub OAuth device flow. Without a configured client id the endpoint is
// honestly unavailable (501), never faked.
func (a *api) handleGitHubDevice(w http.ResponseWriter, r *http.Request) {
	if a.d.Cfg.GithubClientID == "" {
		writeErr(w, http.StatusNotImplemented, "github identity not configured")
		return
	}
	form := url.Values{
		"client_id": {a.d.Cfg.GithubClientID},
		"scope":     {"read:user"},
	}
	var out struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
	}
	if !a.postForm(w, r, a.d.GitHubDeviceURL, form, &out) {
		return
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		writeErr(w, http.StatusBadGateway, "github device flow returned no codes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceCode":      out.DeviceCode,
		"userCode":        out.UserCode,
		"verificationUri": out.VerificationURI,
		"interval":        out.Interval,
	})
}

// handleGitHubPoll implements POST /v1/auth/github/poll {deviceCode}: on
// success it creates/updates the seeder identity and returns the API token
// EXACTLY ONCE — only the sha256 hash is stored (contract C4 identities).
func (a *api) handleGitHubPoll(w http.ResponseWriter, r *http.Request) {
	if a.d.Cfg.GithubClientID == "" {
		writeErr(w, http.StatusNotImplemented, "github identity not configured")
		return
	}
	var body struct {
		DeviceCode string `json:"deviceCode"`
	}
	if !readJSON(w, r, 64<<10, &body) {
		return
	}
	if body.DeviceCode == "" {
		writeErr(w, http.StatusBadRequest, "deviceCode is required")
		return
	}

	form := url.Values{
		"client_id":   {a.d.Cfg.GithubClientID},
		"device_code": {body.DeviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	if a.d.Cfg.GithubClientSecret != "" {
		form.Set("client_secret", a.d.Cfg.GithubClientSecret)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if !a.postForm(w, r, a.d.GitHubTokenURL, form, &tok) {
		return
	}
	switch tok.Error {
	case "":
	case "authorization_pending", "slow_down":
		writeJSON(w, http.StatusAccepted, map[string]string{"status": tok.Error})
		return
	default:
		writeErr(w, http.StatusBadRequest, "github: "+tok.Error)
		return
	}
	if tok.AccessToken == "" {
		writeErr(w, http.StatusBadGateway, "github returned no access token")
		return
	}

	// Resolve the GitHub user behind the token.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.d.GitHubUserURL, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "github request failed")
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := a.d.HTTPClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "github user lookup failed")
		return
	}
	defer resp.Body.Close()
	var user struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user); err != nil || user.Login == "" {
		writeErr(w, http.StatusBadGateway, "github user lookup failed")
		return
	}

	// Mint the API token; persist only its hash. Shown once, by design.
	apiToken, err := newAPIToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	if err := a.d.Store.SaveIdentity(r.Context(), user.Login, user.ID,
		sha256Hex(tok.AccessToken), sha256Hex(apiToken)); err != nil {
		writeErr(w, http.StatusInternalServerError, "saving identity failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"login":    user.Login,
		"apiToken": apiToken,
	})
}

// postForm posts a URL-encoded form expecting a JSON reply (GitHub's
// Accept: application/json mode). Returns false after writing the error.
func (a *api) postForm(w http.ResponseWriter, r *http.Request, endpoint string, form url.Values, out any) bool {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "github request failed")
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.d.HTTPClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "github unreachable")
		return false
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		writeErr(w, http.StatusBadGateway, "github returned invalid JSON")
		return false
	}
	return true
}

// newAPIToken mints a "csx_"-prefixed 192-bit random token.
func newAPIToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "csx_" + hex.EncodeToString(buf), nil
}
