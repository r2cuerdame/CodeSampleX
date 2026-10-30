package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestIntegrationStalledAuthoringClaimsLeaveVerificationAvailable(t *testing.T) {
	pol := testServerPoolPolicy()
	pol.FarmIngestWait = 300 * time.Millisecond
	outside, pg := openTestStore(t, pol)
	now := time.Now().UTC()
	token := "csx_author_v1_" + strings.Repeat("YWFh", 10) + "YWE"
	if err := pg.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{
		TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token))), SessionID: "stalled-author",
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
	poll := func(ctx context.Context) (int, string, error) {
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
		body, err := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), err
	}
	if status, body, err := poll(t.Context()); err != nil || status != http.StatusOK || !strings.Contains(body, `"ASSIGNED"`) {
		t.Fatalf("warm poll: status=%d body=%s err=%v", status, body, err)
	}
	if _, err := outside.Exec(t.Context(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := outside.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "authoring-work\x1fstalled-author"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	release := sync.OnceFunc(func() {
		cancel()
		if _, err := outside.Exec(context.Background(), "ROLLBACK"); err != nil {
			t.Errorf("release authoring fixture: %v", err)
		}
		workers.Wait()
	})
	defer release()
	for range 2 {
		workers.Go(func() { _, _, _ = poll(ctx) })
	}
	deadline := time.Now().Add(3 * time.Second)
	var blocked int
	for time.Now().Before(deadline) {
		if err := outside.QueryRow(t.Context(), `SELECT count(*) FROM pg_locks
			WHERE NOT granted AND locktype='advisory' AND (database,classid,objid,objsubid) IN (
				SELECT database,classid,objid,objsubid FROM pg_locks
				WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted
			)`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stats := pg.PoolStats()
	if blocked != 2 || classStatOf(stats, "farm_ingest").InUse+classStatOf(stats, "authoring").InUse != 2 {
		t.Fatalf("two author claims did not block on the fixture lock: blocked=%d classes=%+v", blocked, stats.Classes)
	}
	for _, path := range []string{"/v1/verification/jobs", "/healthz"} {
		started := time.Now()
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || time.Since(started) > time.Second {
			t.Errorf("%s during stalled author claims: status=%d body=%s elapsed=%v err=%v", path, resp.StatusCode, body, time.Since(started), err)
		}
	}
	started := time.Now()
	if status, body, err := poll(t.Context()); err != nil || status != http.StatusServiceUnavailable || time.Since(started) > 2*time.Second {
		t.Fatalf("excess author poll was not bounded: status=%d body=%s elapsed=%v err=%v", status, body, time.Since(started), err)
	}
	if got := classStatOf(pg.PoolStats(), "farm_ingest"); got.Busy != 0 || got.Timeouts != 0 {
		t.Errorf("authoring pressure spilled into verification: %+v", got)
	}
	release()
	if status, body, err := poll(t.Context()); err != nil || status != http.StatusOK || !strings.Contains(body, `"ASSIGNED"`) {
		t.Fatalf("authoring did not recover after lock release: status=%d body=%s err=%v", status, body, err)
	}
}

func TestIntegrationFarmGovernorPauseDoesNotRefuseAuthoringPoll(t *testing.T) {
	pol := testServerPoolPolicy()
	_, pg := openTestStore(t, pol)
	now := time.Now().UTC()
	token := "csx_author_v1_" + strings.Repeat("YWFh", 10) + "YWE"
	if err := pg.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{
		TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token))), SessionID: "paused-farm-author",
		Label: "fixture-author", Model: "fixture", Reasoning: "fixture", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	pg.SetFarmIngestConns(0)
	srv := httptest.NewServer(withDBBudget(httpapi.NewMux(httpapi.Deps{Store: pg})))
	defer srv.Close()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/authoring/work/next", strings.NewReader(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("author poll while Farm ingest paused: status=%d body=%s err=%v", resp.StatusCode, body, err)
	}
	resp, err = srv.Client().Get(srv.URL + "/v1/verification/jobs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Farm ingest pause stopped refusing verification: status=%d", resp.StatusCode)
	}
}
