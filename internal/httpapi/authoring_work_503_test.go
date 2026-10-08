package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestAuthoringWork503CarriesMatchingReasonAndRequestID(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)

	cases := []struct {
		name, stage, cause string
		err                error
	}{
		{"refresh pool", "session_refresh", "pool_busy", serverstore.ErrPoolBusy},
		{"scan deadline", "candidate_scan", "deadline", context.DeadlineExceeded},
		{"held canceled", "held_work", "canceled", context.Canceled},
		{"completeness query", "completeness", "query_timeout", &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}},
		{"cli completeness pool", "cli_completeness", "pool_busy", serverstore.ErrPoolBusy},
		{"claim pool", "claim", "pool_busy", serverstore.ErrPoolBusy},
	}
	ids := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			w := httptest.NewRecorder()
			if !writeAuthoringWorkBusy(w, tc.err, tc.stage) {
				t.Fatal("busy error was not handled")
			}
			assertAuthoring503Receipt(t, w, &logs, tc.stage+"."+tc.cause, ids)
		})
	}
	logs.Reset()
	w := httptest.NewRecorder()
	(&api{}).handleAuthoringWorkNext(w, httptest.NewRequest(http.MethodPost, "/v1/authoring/work/next", nil))
	assertAuthoring503Receipt(t, w, &logs, "storage_unavailable", ids)
	if writeAuthoringWorkBusy(httptest.NewRecorder(), errors.New("other"), "claim") {
		t.Fatal("ordinary error became 503")
	}
}

type authoring503StageStore struct {
	*serverstore.Fake
	stage string
}

func (s *authoring503StageStore) RefreshAuthoringSession(ctx context.Context, tokenHash, ip, computerName string, now, idleExpiresAt time.Time) (serverstore.AuthoringSessionRow, error) {
	if s.stage == "session_refresh" {
		return serverstore.AuthoringSessionRow{}, serverstore.ErrPoolBusy
	}
	return s.Fake.RefreshAuthoringSession(ctx, tokenHash, ip, computerName, now, idleExpiresAt)
}

func (s *authoring503StageStore) ListAuthoringExpansionCandidates(ctx context.Context, limit int) ([]serverstore.WantedRow, error) {
	if s.stage == "candidate_scan" {
		return nil, context.DeadlineExceeded
	}
	return s.Fake.ListAuthoringExpansionCandidates(ctx, limit)
}

func (s *authoring503StageStore) AuthoringWorkForSubmission(ctx context.Context, sessionID, sampleID string, now time.Time) (serverstore.AuthoringWorkRow, bool, error) {
	if s.stage == "held_work" {
		return serverstore.AuthoringWorkRow{}, false, serverstore.ErrPoolBusy
	}
	return s.Fake.AuthoringWorkForSubmission(ctx, sessionID, sampleID, now)
}

func (s *authoring503StageStore) FilterIncompleteAuthoringCandidates(ctx context.Context, rows []serverstore.WantedRow) ([]serverstore.WantedRow, error) {
	if s.stage == "completeness" {
		return nil, serverstore.ErrPoolBusy
	}
	return s.Fake.FilterIncompleteAuthoringCandidates(ctx, rows)
}

func (s *authoring503StageStore) FilterUnobservedCLIWork(ctx context.Context, rows []serverstore.WantedRow, now time.Time) ([]serverstore.WantedRow, error) {
	if s.stage == "cli_completeness" {
		return nil, serverstore.ErrPoolBusy
	}
	return s.Fake.FilterUnobservedCLIWork(ctx, rows, now)
}

func (s *authoring503StageStore) ClaimAuthoringWork(ctx context.Context, sessionID string, rows []serverstore.WantedRow, now, leaseExpiresAt time.Time) (serverstore.AuthoringWorkRow, bool, error) {
	if s.stage == "claim" {
		return serverstore.AuthoringWorkRow{}, false, serverstore.ErrPoolBusy
	}
	return s.Fake.ClaimAuthoringWork(ctx, sessionID, rows, now, leaseExpiresAt)
}

func TestAuthoringWork503StageReceiptsOnHTTPRoute(t *testing.T) {
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	for _, tc := range []struct{ stage, cause string }{
		{"session_refresh", "pool_busy"},
		{"candidate_scan", "deadline"},
		{"held_work", "pool_busy"},
		{"completeness", "pool_busy"},
		{"cli_completeness", "pool_busy"},
		{"claim", "pool_busy"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			store := &authoring503StageStore{Fake: serverstore.NewFake(), stage: tc.stage}
			authoringSession(t, store.Fake, token, "writer-503", testNow)
			srv := httptest.NewServer(NewMux(Deps{Store: store, Now: func() time.Time { return testNow }}))
			defer srv.Close()
			envelope := `{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`
			if tc.stage == "cli_completeness" {
				envelope = cliLinuxEnvelope
			}
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next", strings.NewReader(envelope))
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("X-CSX-503-Reason") != tc.stage+"."+tc.cause || resp.Header.Get("X-CSX-Request-ID") == "" {
				t.Fatalf("status=%d reason=%q id=%q", resp.StatusCode, resp.Header.Get("X-CSX-503-Reason"), resp.Header.Get("X-CSX-Request-ID"))
			}
		})
	}
}

func assertAuthoring503Receipt(t *testing.T, w *httptest.ResponseRecorder, logs *bytes.Buffer, reason string, ids map[string]bool) {
	t.Helper()
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	id := w.Header().Get("X-CSX-Request-ID")
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) || ids[id] {
		t.Fatalf("request id %q is invalid or reused", id)
	}
	ids[id] = true
	if got := w.Header().Get("X-CSX-503-Reason"); got != reason {
		t.Fatalf("reason = %q, want %q", got, reason)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Fatalf("invalid error body: %q: %v", w.Body.String(), err)
	}
	if got := logs.String(); !strings.Contains(got, "request_id="+id+" reason="+reason) {
		t.Fatalf("response receipt missing from log: %q", got)
	}
}
