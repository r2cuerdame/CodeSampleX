package admin

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestAuthoringSessionRequiresHourlyRefreshWithoutAbsoluteLimit(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	r := newAuthoringRegistry(func() time.Time { return now })
	r.random = strings.NewReader(strings.Repeat("a", 44))
	grant, err := r.Issue("sample-worker-01", "agy", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(grant.Token, authoringTokenPrefix) {
		t.Fatalf("token = %q", grant.Token)
	}
	if got := grant.IdleExpiresAt.Sub(now); got != time.Hour {
		t.Fatalf("idle lifetime = %s", got)
	}
	now = now.Add(59 * time.Minute)
	refreshed, err := r.Refresh(grant.Token, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed.IdleExpiresAt.Sub(now); got != time.Hour {
		t.Fatalf("refreshed idle lifetime = %s", got)
	}

	now = refreshed.IdleExpiresAt
	if _, err := r.Refresh(grant.Token, ""); !errors.Is(err, errAuthoringExpired) {
		t.Fatalf("refresh at exact idle expiry = %v", err)
	}
}

func TestAuthoringSessionSurvivesRegistryRestartWithHashOnlyStore(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	store := serverstore.NewFake()
	first := newAuthoringRegistry(func() time.Time { return now }, store)
	first.random = strings.NewReader(strings.Repeat("p", 44))
	grant, err := first.Issue("persistent-worker", "agy", "medium")
	if err != nil {
		t.Fatal(err)
	}

	// A fresh registry models a server process restart. It has no in-memory
	// map entry, but the hashed capability and metadata remain usable.
	second := newAuthoringRegistry(func() time.Time { return now }, store)
	now = now.Add(45 * time.Minute)
	refreshed, err := second.RefreshContext(t.Context(), grant.Token, "203.0.113.44", "worker-host-01")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ID != grant.ID || refreshed.IdleExpiresAt != now.Add(time.Hour) {
		t.Fatalf("refreshed after restart = %+v", refreshed)
	}
	views, err := second.ListContext(t.Context())
	if err != nil || len(views) != 1 || views[0].LastIP != "203.0.113.44" || views[0].ComputerName != "worker-host-01" {
		t.Fatalf("persisted views = %+v, err=%v", views, err)
	}
}

func TestAuthoringSessionsAreIndependentAndRevocableByID(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	r := newAuthoringRegistry(func() time.Time { return now })
	r.random = strings.NewReader(strings.Repeat("a", 44) + strings.Repeat("b", 44))
	first, err := r.Issue("desktop-a", "agy", "low")
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Issue("desktop-b", "codex", "high")
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token {
		t.Fatal("rotation reused the token")
	}
	if sessions := r.List(); len(sessions) != 2 {
		t.Fatalf("active sessions = %d, want 2", len(sessions))
	}
	if err := r.RevokeID(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Refresh(first.Token, ""); !errors.Is(err, errAuthoringInvalid) {
		t.Fatalf("revoked token refresh = %v", err)
	}
	if _, err := r.Refresh(second.Token, "203.0.113.3"); err != nil {
		t.Fatalf("other session was revoked: %v", err)
	}
	sessions := r.List()
	if len(sessions) != 1 || sessions[0].Label != "desktop-b" || sessions[0].Model != "codex" || sessions[0].Reasoning != "high" || sessions[0].LastIP != "203.0.113.3" {
		t.Fatalf("remaining sessions = %+v", sessions)
	}
}

func TestAuthoringSessionRotateInvalidatesOldToken(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	r := newAuthoringRegistry(func() time.Time { return now })
	r.random = strings.NewReader(strings.Repeat("a", 44) + strings.Repeat("b", 32))
	issued, err := r.Issue("worker-a", "claude-haiku", "low")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	rotated, err := r.RotateIDContext(t.Context(), issued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ID != issued.ID || rotated.Token == issued.Token || rotated.IdleExpiresAt != now.Add(time.Hour) {
		t.Fatalf("rotated grant = %+v", rotated)
	}
	if _, err := r.Refresh(issued.Token, ""); !errors.Is(err, errAuthoringInvalid) {
		t.Fatalf("old token refresh = %v", err)
	}
	if _, err := r.Refresh(rotated.Token, "203.0.113.7"); err != nil {
		t.Fatalf("new token refresh = %v", err)
	}
}

func TestAuthoringTokenIsStrictAndPromptStopsBeforePublish(t *testing.T) {
	for _, token := range []string{"", "csx_bad", authoringTokenPrefix + "not/base64", authoringTokenPrefix + "YQ"} {
		if _, ok := validAuthoringToken(token); ok {
			t.Fatalf("accepted malformed token %q", token)
		}
	}
	prompt := authoringPrompt("https://codesamplex.dev/", authoringGrant{Token: "sentinel", Label: "worker-laptop", Model: "agy", Reasoning: "auto"})
	for _, want := range []string{
		`csx sample-worker refresh --server "https://codesamplex.dev" --token "sentinel"`,
		"45분마다", "5분 기다린 뒤 다시 호출", "2번으로 돌아가 다음 일감", "실패 관측과 사용량으로 새 Finding·커버리지 일감", "worker-laptop", "agy", "auto", "CSX_HOME", "search_known_solution", "run_observed_command",
		"csx sample create", "csx sample verify", "csx sample preview", "csx sample publish를 실행하지 않는다",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestAuthoringWindowsCMDPollsForeverAndLaunchesIsolatedAGY(t *testing.T) {
	grant := authoringGrant{ID: "Session-123", Token: "sentinel", Label: `lab & echo unsafe`, Model: "agy", Reasoning: "high"}
	script := authoringWindowsCMD("https://codesamplex.dev/", grant)
	for _, want := range []string{
		"@echo off", "setlocal EnableExtensions DisableDelayedExpansion", `set "CSX_SESSION_ID=session-123"`,
		`set "CSX_HOME=%LOCALAPPDATA%\CodeSampleX\sample-workers\%CSX_SESSION_ID%"`, ":poll", "sample-worker refresh",
		"sample-worker next", `findstr /b /c:"NO_WORK:"`, `timeout /t 300 /nobreak`, "--dangerously-skip-permissions", "--print", "CSX_AGY_LOG", "Tee-Object", "goto :poll",
		"HTTP 410", "download a new CMD file",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("CMD missing %q", want)
		}
	}
	if strings.Contains(script, "--prompt-interactive") {
		t.Fatal("CMD must let each AGY turn exit so the supervisor can poll again")
	}
	if !strings.Contains(script, `@('--dangerously-skip-permissions','--print-timeout','50m','--print',$prompt)`) {
		t.Fatal("--print consumes the next argument; the prompt must be the final AGY argument")
	}
	if !strings.Contains(script, `if($env:CSX_REASONING -in @('low','medium','high'))`) || strings.Contains(script, `--effort $env:CSX_REASONING`) {
		t.Fatal("auto reasoning must omit --effort; AGY accepts only low, medium, or high")
	}
	if strings.Contains(script, grant.Label) {
		t.Fatal("untrusted label was interpolated into CMD syntax")
	}
	var encoded strings.Builder
	for _, line := range strings.Split(script, "\r\n") {
		if !strings.HasPrefix(line, `set "CSX_PROMPT_B64_`) {
			continue
		}
		separator := strings.IndexByte(line, '=')
		if separator < 0 || !strings.HasSuffix(line, `"`) {
			t.Fatalf("malformed prompt chunk %q", line)
		}
		encoded.WriteString(line[separator+1 : len(line)-1])
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded.String())
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(decoded)
	for _, want := range []string{"같은 현재 임대를 확인", "사용량 기반 커버리지 확장", "search_known_solution", "run_observed_command", "sample-worker submit <sampleId>", "바깥 CMD supervisor"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("CMD agent prompt missing %q", want)
		}
	}
	if got := authoringWindowsCMD("https://codesamplex.dev", authoringGrant{Model: "codex"}); got != "" {
		t.Fatalf("non-AGY CMD = %q", got)
	}
}

func TestAuthoringLinuxSHPollsForeverAndLaunchesIsolatedAGY(t *testing.T) {
	grant := authoringGrant{ID: "Session-456", Token: "sentinel", Label: `lab; rm unsafe`, Model: "agy", Reasoning: "high"}
	script := authoringLinuxSH("https://codesamplex.dev/", grant)
	for _, want := range []string{
		"#!/usr/bin/env bash", `CSX_SESSION_ID='session-456'`, `export CSX_HOME="$HOME/.local/share/CodeSampleX/sample-workers/$CSX_SESSION_ID"`,
		`cd "$CSX_WORKSPACE"`, "while true; do", "sample-worker refresh", "sample-worker next", `grep -q -e '^NO_WORK:' -e 'No uncovered Wanted work'`, "sleep 300",
		"--dangerously-skip-permissions", "--print-timeout 50m", `agy "${agy_args[@]}" --print "$prompt"`, "PIPESTATUS[0]",
		"HTTP 410", "download a new SH file",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("Linux SH missing %q", want)
		}
	}
	if strings.Contains(script, grant.Label) || strings.Contains(script, "--prompt-interactive") {
		t.Fatal("Linux SH interpolated an untrusted label or kept AGY interactive")
	}
	prefix := "CSX_PROMPT_B64='"
	start := strings.Index(script, prefix)
	if start < 0 {
		t.Fatal("Linux SH prompt missing")
	}
	start += len(prefix)
	end := strings.Index(script[start:], "'\n")
	if end < 0 {
		t.Fatal("Linux SH prompt terminator missing")
	}
	decoded, err := base64.StdEncoding.DecodeString(script[start : start+end])
	if err != nil || !strings.Contains(string(decoded), "Linux shell supervisor") {
		t.Fatalf("Linux SH prompt = %q err=%v", decoded, err)
	}
	if got := authoringLinuxSH("https://codesamplex.dev", authoringGrant{Model: "codex"}); got != "" {
		t.Fatalf("non-AGY Linux SH = %q", got)
	}
}

func TestAuthoringLinuxSHParsesInBash(t *testing.T) {
	script := authoringLinuxSH("https://codesamplex.dev", authoringGrant{
		ID: "session-parse", Token: "sentinel", Label: "parse", Model: "agy", Reasoning: "low",
	})
	name := "bash"
	args := []string{"-n"}
	if runtime.GOOS == "windows" {
		listed, err := exec.Command("wsl.exe", "--list", "--quiet").CombinedOutput()
		if err != nil || len(bytes.TrimSpace(listed)) == 0 {
			t.Skip("WSL bash parser test requires an installed distribution")
		}
		name = "wsl.exe"
		args = []string{"bash", "-n"}
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, output)
	}
}

func TestAuthoringWindowsCMDParsesInWindowsCommandProcessor(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows CMD parser test")
	}
	temp := t.TempDir()
	script := authoringWindowsCMD("https://codesamplex.dev", authoringGrant{
		ID: "session-parse", Token: "sentinel", Label: "parse", Model: "agy", Reasoning: "low",
	})
	path := filepath.Join(temp, "worker.cmd")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("cmd.exe", "/d", "/c", path)
	command.Env = []string{
		"ComSpec=" + os.Getenv("ComSpec"), "LOCALAPPDATA=" + temp,
		"PATH=" + filepath.Join(os.Getenv("SystemRoot"), "System32"), "SystemRoot=" + os.Getenv("SystemRoot"), "TEMP=" + temp,
	}
	output, err := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("CMD parse exit=%v output=%s", err, output)
	}
	if !strings.Contains(string(output), "AGY was not found") {
		t.Fatalf("CMD did not reach the expected parsed branch: %s", output)
	}
}
