package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestAdminIssuesListsRefreshesAndRevokesMultipleSampleWorkers(t *testing.T) {
	mux, secret := configuredMux(t, &fakeStore{})
	issueBody := `{"label":"spring-lab","model":"agy","reasoning":"auto","count":2}`
	issue := httptest.NewRequest(http.MethodPost, "/admin/api/authoring-sessions", strings.NewReader(issueBody))
	issue.SetBasicAuth("recuerdame", secret)
	issue.Header.Set("Origin", "https://codesamplex.dev")
	issue.Header.Set("Content-Type", "application/json")
	issue.Header.Set("X-CSX-CSRF", "1")
	issue.RemoteAddr = "198.51.100.8:443"
	issued := httptest.NewRecorder()
	mux.ServeHTTP(issued, issue)
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue status = %d, body=%s", issued.Code, issued.Body.String())
	}
	if strings.Contains(issued.Body.String(), `"token"`) {
		t.Fatal("raw token was returned as a standalone JSON field")
	}
	var response struct {
		Prompt  string `json:"prompt"`
		Workers []struct {
			Command    string `json:"command"`
			WindowsCMD string `json:"windowsCmd"`
			LinuxSH    string `json:"linuxSh"`
			Session    struct {
				ID    string `json:"sessionId"`
				Label string `json:"label"`
				Model string `json:"model"`
			} `json:"session"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Workers) != 2 || response.Workers[0].Session.Label != "spring-lab-01" || response.Workers[1].Session.Label != "spring-lab-02" {
		t.Fatalf("workers = %+v", response.Workers)
	}
	if !strings.Contains(response.Workers[0].WindowsCMD, "@echo off") || !strings.Contains(response.Workers[0].WindowsCMD, "--dangerously-skip-permissions") {
		t.Fatalf("worker CMD was not returned: %q", response.Workers[0].WindowsCMD)
	}
	if !strings.Contains(response.Workers[0].LinuxSH, "#!/usr/bin/env bash") || !strings.Contains(response.Workers[0].LinuxSH, "--dangerously-skip-permissions") {
		t.Fatalf("worker Linux SH was not returned: %q", response.Workers[0].LinuxSH)
	}
	for _, want := range []string{"SAMPLE WORKER 1/2", "SAMPLE WORKER 2/2", "agy", "csx sample-worker refresh", "csx sample-worker next", "SAMPLE", "EVIDENCE", "DEPENDENCY", "csx sample-worker submit <sampleId>"} {
		if !strings.Contains(response.Prompt, want) {
			t.Errorf("combined prompt missing %q", want)
		}
	}

	tokenMatch := regexp.MustCompile(`--token "([^"]+)"`).FindStringSubmatch(response.Workers[0].Command)
	if len(tokenMatch) != 2 {
		t.Fatalf("complete CLI command = %q", response.Workers[0].Command)
	}
	rotate := httptest.NewRequest(http.MethodPost, "/admin/api/authoring-sessions/"+response.Workers[0].Session.ID+"/rotate", strings.NewReader("{}"))
	rotate.SetBasicAuth("recuerdame", secret)
	rotate.Header.Set("Origin", "https://codesamplex.dev")
	rotate.Header.Set("Content-Type", "application/json")
	rotate.Header.Set("X-CSX-CSRF", "1")
	rotated := httptest.NewRecorder()
	mux.ServeHTTP(rotated, rotate)
	if rotated.Code != http.StatusOK || strings.Contains(rotated.Body.String(), `"token"`) {
		t.Fatalf("rotate status=%d body=%s", rotated.Code, rotated.Body.String())
	}
	var rotatedResponse struct {
		Prompt string `json:"prompt"`
		Worker struct {
			Command    string `json:"command"`
			WindowsCMD string `json:"windowsCmd"`
			LinuxSH    string `json:"linuxSh"`
		} `json:"worker"`
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &rotatedResponse); err != nil {
		t.Fatal(err)
	}
	rotatedToken := regexp.MustCompile(`--token "([^"]+)"`).FindStringSubmatch(rotatedResponse.Worker.Command)
	if len(rotatedToken) != 2 || rotatedToken[1] == tokenMatch[1] || rotatedResponse.Prompt == "" || !strings.Contains(rotatedResponse.Worker.WindowsCMD, rotatedToken[1]) || strings.Contains(rotatedResponse.Worker.WindowsCMD, tokenMatch[1]) || !strings.Contains(rotatedResponse.Worker.LinuxSH, rotatedToken[1]) || strings.Contains(rotatedResponse.Worker.LinuxSH, tokenMatch[1]) {
		t.Fatalf("rotated response = %+v", rotatedResponse)
	}
	oldRefresh := httptest.NewRequest(http.MethodPost, "/v1/authoring/session/refresh", strings.NewReader("{}"))
	oldRefresh.Header.Set("Authorization", "Bearer "+tokenMatch[1])
	oldRefresh.Header.Set("Content-Type", "application/json")
	oldRefreshRec := httptest.NewRecorder()
	mux.ServeHTTP(oldRefreshRec, oldRefresh)
	if oldRefreshRec.Code != http.StatusUnauthorized {
		t.Fatalf("old token refresh status = %d", oldRefreshRec.Code)
	}

	refresh := httptest.NewRequest(http.MethodPost, "/v1/authoring/session/refresh", strings.NewReader("{}"))
	refresh.Header.Set("Authorization", "Bearer "+rotatedToken[1])
	refresh.Header.Set("Content-Type", "application/json")
	refresh.RemoteAddr = "198.51.100.9:443"
	refreshed := httptest.NewRecorder()
	mux.ServeHTTP(refreshed, refresh)
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body=%s", refreshed.Code, refreshed.Body.String())
	}

	revoke := httptest.NewRequest(http.MethodDelete, "/admin/api/authoring-sessions/"+response.Workers[0].Session.ID, strings.NewReader("{}"))
	revoke.SetBasicAuth("recuerdame", secret)
	revoke.Header.Set("Origin", "https://codesamplex.dev")
	revoke.Header.Set("Content-Type", "application/json")
	revoke.Header.Set("X-CSX-CSRF", "1")
	revoked := httptest.NewRecorder()
	mux.ServeHTTP(revoked, revoke)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, body=%s", revoked.Code, revoked.Body.String())
	}

	refreshAgain := httptest.NewRequest(http.MethodPost, "/v1/authoring/session/refresh", strings.NewReader("{}"))
	refreshAgain.Header.Set("Authorization", "Bearer "+rotatedToken[1])
	refreshAgain.Header.Set("Content-Type", "application/json")
	refreshedAgain := httptest.NewRecorder()
	mux.ServeHTTP(refreshedAgain, refreshAgain)
	if refreshedAgain.Code != http.StatusUnauthorized {
		t.Fatalf("revoked refresh status = %d", refreshedAgain.Code)
	}
}

func TestAuthoringRefreshLimiterBoundsIPAndTokenFloods(t *testing.T) {
	limiter := newAuthoringRateLimiter()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	for i := 0; i < authoringTokenRefreshLimit; i++ {
		if !limiter.allow("token:known", now, authoringTokenRefreshLimit) {
			t.Fatalf("known token rejected at request %d", i+1)
		}
	}
	if limiter.allow("token:known", now, authoringTokenRefreshLimit) {
		t.Fatal("known token flood was not limited")
	}
	for i := 0; i < authoringIPRefreshLimit; i++ {
		if !limiter.allow("ip:198.51.100.4", now, authoringIPRefreshLimit) {
			t.Fatalf("IP rejected at request %d", i+1)
		}
	}
	if limiter.allow("ip:198.51.100.4", now, authoringIPRefreshLimit) {
		t.Fatal("invalid-token IP flood was not limited")
	}
	if !limiter.allow("token:known", now.Add(authoringRateWindow), authoringTokenRefreshLimit) {
		t.Fatal("token stayed blocked after the fixed window")
	}
}

func TestAdminAuthoringIssueRequiresBasicOriginCSRFAndJSON(t *testing.T) {
	mux, secret := configuredMux(t, &fakeStore{})
	tests := []struct {
		name, user, origin, csrf, contentType string
		want                                  int
	}{
		{name: "no auth", origin: "https://codesamplex.dev", csrf: "1", contentType: "application/json", want: http.StatusUnauthorized},
		{name: "wrong origin", user: "recuerdame", origin: "https://evil.example", csrf: "1", contentType: "application/json", want: http.StatusForbidden},
		{name: "no csrf", user: "recuerdame", origin: "https://codesamplex.dev", contentType: "application/json", want: http.StatusForbidden},
		{name: "form body", user: "recuerdame", origin: "https://codesamplex.dev", csrf: "1", contentType: "application/x-www-form-urlencoded", want: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/admin/api/authoring-sessions", strings.NewReader(`{"label":"lab","model":"agy","reasoning":"auto","count":1}`))
			if tc.user != "" {
				req.SetBasicAuth(tc.user, secret)
			}
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-CSX-CSRF", tc.csrf)
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestAdminPageContainsSampleWorkerControlsButNoSessionToken(t *testing.T) {
	mux, secret := configuredMux(t, &fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"내부 샘플 워커", `name="model"`, `name="reasoning"`, `name="count"`, "프롬프트 + CLI 발급·복사", "Windows CMD 또는 Linux/WSL SH", `src="/admin/admin.js"`} {
		if !strings.Contains(body, want) {
			t.Errorf("admin page missing %q", want)
		}
	}
	if strings.Contains(body, authoringTokenPrefix) {
		t.Fatal("dashboard HTML contains a session token")
	}
}

func TestAdminListsOnlyBoundedPrivateDraftMetadata(t *testing.T) {
	store := serverstore.NewFake()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SaveAuthoringDraft(context.Background(), serverstore.AuthoringDraftRow{
		SampleID: "sha256:private-only", SessionID: "session-secret", WorkerLabel: "java-01",
		ManifestJSON: `{"packages":["pkg:maven/org.example/lib@1.0.0"],"symbols":["Example.call"],"case":{"goal":"prove the Java call"},"privateSource":"must-not-leak"}`,
		LocalStatus:  "LOCAL_PASS", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	secret := "a-long-random-admin-secret"
	mux := http.NewServeMux()
	if !Register(mux, Deps{Store: &fakeStore{}, Authoring: store, TokenSHA256: digest(secret), PublicURL: "https://codesamplex.dev", Now: func() time.Time { return now }}) {
		t.Fatal("register")
	}
	// The draft inbox restated per-draft what the farm panel now reports per
	// worker, so the panel went and the endpoint with it. This test guarded the
	// boundary that endpoint had to hold — bounded metadata out, session
	// secrets and private source never — and the strongest form of that
	// guarantee is that nothing serves drafts at all.
	req := httptest.NewRequest(http.MethodGet, "/admin/api/authoring-drafts", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("draft metadata is still served: status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, forbidden := range []string{"sha256:private-only", "session-secret", "privateSource", "must-not-leak", "tokenHash"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("response leaked %q", forbidden)
		}
	}
}

func TestAdminScriptOffersPerWorkerRecopy(t *testing.T) {
	mux, secret := configuredMux(t, &fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "/admin/admin.js", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{"프롬프트 + CLI 재복사", "무한 CMD 내려받기", "/rotate", "data.worker.windowsCmd", "CMD를 내려받았습니다", "이전 프롬프트와 명령은 무효", "URL.createObjectURL"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("admin script missing %q", want)
		}
	}
}
