package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A sample writer could take work and submit a sample. It had no way at all to
// say "there is nothing here to write", so the only thing the server ever
// learned about a hopeless coordinate was silence — and silence is exactly
// what a busy worker looks like. Classifying the failure is the whole
// difference between a registry that was down for ten minutes and an artifact
// that contains no code.

func authoringSession(t *testing.T, store *serverstore.Fake, token, sessionID string, now time.Time) {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	if err := store.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{
		TokenHash: hex.EncodeToString(sum[:]), SessionID: sessionID, Label: sessionID,
		Model: "claude-haiku", Reasoning: "low", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
}

func claimWork(t *testing.T, srv, token string) (string, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv+"/v1/authoring/work/next",
		bytes.NewBufferString(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var assigned struct {
		Status string `json:"status"`
		Work   struct {
			Package string `json:"package"`
			Symbol  string `json:"symbol"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned.Status != "ASSIGNED" {
		t.Fatalf("work status = %q, want ASSIGNED", assigned.Status)
	}
	return assigned.Work.Package, assigned.Work.Symbol
}

func reportOutcome(t *testing.T, srv, token, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv+"/v1/authoring/work/outcome", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func TestAWriterCanHandBackWorkItMeasuredImpossible(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "writer-a", testNow)
	if err := store.RecordWanted(t.Context(), testNow.Format("2006-01-02"), "0123456789abcdef", []serverstore.WantedRow{{
		Ecosystem: "maven", Name: "org.jetbrains.kotlin/kotlin-gradle-plugins-bom", Version: "2.2.20", Symbol: "Bom",
	}}); err != nil {
		t.Fatal(err)
	}
	pkg, _ := claimWork(t, srv.URL, token)
	if pkg == "" {
		t.Fatal("no work assigned")
	}

	status, body := reportOutcome(t, srv.URL, token,
		`{"schemaVersion":1,"outcome":"NO_CALLABLE_SYMBOL","detail":"pom-only artifact: no jar, no classes"}`)
	if status != http.StatusOK {
		t.Fatalf("report status = %d, want 200 (%v)", status, body)
	}
	if body["status"] != "RELEASED" {
		t.Fatalf("report = %v, want RELEASED", body)
	}
	state, found, err := store.AuthoringAttemptState(t.Context(),
		"maven", "org.jetbrains.kotlin/kotlin-gradle-plugins-bom", "2.2.20", "Bom")
	if err != nil || !found {
		t.Fatalf("attempt state: found=%v err=%v", found, err)
	}
	if state.SessionsMeasuringImpossible != 1 {
		t.Errorf("sessionsMeasuringImpossible = %d, want 1", state.SessionsMeasuringImpossible)
	}
	last := state.History[len(state.History)-1]
	if last.Outcome != serverstore.AuthoringNoCallableSymbol || last.Detail == "" {
		t.Errorf("last history entry = %+v, want the classified report with its note", last)
	}
}

func TestAnOutcomeReportWithNoClaimIsNotAnError(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "writer-a", testNow)
	status, body := reportOutcome(t, srv.URL, token, `{"schemaVersion":1,"outcome":"TRANSIENT"}`)
	if status != http.StatusOK || body["status"] != "NO_CLAIM" {
		t.Fatalf("status=%d body=%v, want 200 NO_CLAIM", status, body)
	}
}

// AUTHORED and HANDED_OUT are the server's own bookkeeping. A client that
// could report them could mark a coordinate solved without writing anything.
func TestAWriterCannotReportTheServersOwnOutcomes(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "writer-a", testNow)
	for _, outcome := range []string{"AUTHORED", "HANDED_OUT", "", "whatever"} {
		status, _ := reportOutcome(t, srv.URL, token,
			`{"schemaVersion":1,"outcome":"`+outcome+`"}`)
		if status != http.StatusBadRequest {
			t.Errorf("outcome %q accepted with status %d", outcome, status)
		}
	}
}

func TestAnOutcomeReportNeedsASession(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	status, _ := reportOutcome(t, srv.URL, "csx_author_v1_bm9wZW5vcGVub3Blbm9wZW5vcGVub3Blbm9wZQ",
		`{"schemaVersion":1,"outcome":"TRANSIENT"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

// The whole point of handing the claim back is that the slot is free again.
func TestReleasedWorkIsImmediatelyOfferedToAnotherWriter(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const tokenA = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	const tokenB = "csx_author_v1_YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI"
	authoringSession(t, store, tokenA, "writer-a", testNow)
	authoringSession(t, store, tokenB, "writer-b", testNow)
	if err := store.RecordWanted(t.Context(), testNow.Format("2006-01-02"), "0123456789abcdef", []serverstore.WantedRow{{
		Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post",
	}}); err != nil {
		t.Fatal(err)
	}
	pkg, _ := claimWork(t, srv.URL, tokenA)
	if status, body := reportOutcome(t, srv.URL, tokenA,
		`{"schemaVersion":1,"outcome":"TRANSIENT","detail":"registry 503"}`); status != http.StatusOK || body["status"] != "RELEASED" {
		t.Fatalf("release status=%d body=%v", status, body)
	}
	if again, _ := claimWork(t, srv.URL, tokenB); again != pkg {
		t.Fatalf("second writer got %q, want the released %q", again, pkg)
	}
}
