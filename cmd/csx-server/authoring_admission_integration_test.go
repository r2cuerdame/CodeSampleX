package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/httpapi"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestIntegrationAuthoringPollSurvivesBackgroundSaturation(t *testing.T) {
	pol := testServerPoolPolicy()
	pol.BackgroundConns = 1
	outside, pg := openTestStore(t, pol)
	now := time.Now().UTC()
	token := "csx_author_v1_" + strings.Repeat("YWFh", 10) + "YWE"
	if err := pg.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{
		TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token))), SessionID: "admission-author",
		Label: "fixture-author", Model: "fixture", Reasoning: "fixture", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	if err := pg.RecordWanted(t.Context(), now.Format("2006-01-02"), "0123456789abcdef", []serverstore.WantedRow{{
		Ecosystem: "npm", Name: "fixture-work", Version: "1.0.0", Symbol: "run",
	}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(withDBBudget(httpapi.NewMux(httpapi.Deps{Store: pg})))
	defer srv.Close()
	poll := func() (int, string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/authoring/work/next", strings.NewReader(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
		if err != nil {
			return 0, "", err
		}
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(r)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		var result struct{ Status string }
		err = json.NewDecoder(resp.Body).Decode(&result)
		return resp.StatusCode, result.Status, err
	}
	if status, assignment, err := poll(); err != nil || status != http.StatusOK || assignment != "ASSIGNED" {
		t.Fatalf("warm poll: status=%d assignment=%s err=%v", status, assignment, err)
	}
	if _, err := outside.Exec(t.Context(), "BEGIN; LOCK TABLE stats_daily IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = outside.Exec(context.Background(), "ROLLBACK") })
	done := make(chan error, 1)
	go func() {
		done <- pg.SetStatsDaily(context.Background(), now.Format("2006-01-02"), "{}")
	}()
	defer func() {
		if _, err := outside.Exec(context.Background(), "ROLLBACK"); err != nil {
			t.Errorf("release background fixture: %v", err)
		}
		if err := <-done; err != nil {
			t.Errorf("background fixture: %v", err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for classStatOf(pg.PoolStats(), "background").InUse != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if classStatOf(pg.PoolStats(), "background").InUse != 1 {
		t.Fatal("background fixture did not saturate its admission capacity")
	}
	if status, assignment, err := poll(); err != nil || status != http.StatusOK || assignment != "ASSIGNED" {
		t.Fatalf("authoring poll starved behind background work: status=%d assignment=%s err=%v", status, assignment, err)
	}
}
