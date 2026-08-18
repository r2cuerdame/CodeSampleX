package admin

import (
	"errors"
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

func TestAuthoringTokenIsStrictAndPromptStopsBeforePublish(t *testing.T) {
	for _, token := range []string{"", "csx_bad", authoringTokenPrefix + "not/base64", authoringTokenPrefix + "YQ"} {
		if _, ok := validAuthoringToken(token); ok {
			t.Fatalf("accepted malformed token %q", token)
		}
	}
	prompt := authoringPrompt("https://codesamplex.dev/", authoringGrant{Token: "sentinel", Label: "worker-laptop", Model: "agy", Reasoning: "auto"})
	for _, want := range []string{
		`csx sample-worker refresh --server "https://codesamplex.dev" --token "sentinel"`,
		"45분마다", "worker-laptop", "agy", "auto", "CSX_HOME", "search_known_solution", "run_observed_command",
		"csx sample create", "csx sample verify", "csx sample preview", "csx sample publish를 실행하지 않는다",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
