package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Work leaving the board silently is the failure mode this panel exists for.
// 1,113 samples were withdrawn in production with a reason recorded against
// every one of them, and seeing why meant opening a database. Withheld
// coordinates must not repeat that: the reason, the evidence, the age and the
// way back all belong where the operator already looks.

func withheldMux(t *testing.T, store *serverstore.Fake, now time.Time) (*http.ServeMux, string) {
	t.Helper()
	secret := "a-long-random-admin-secret"
	mux := http.NewServeMux()
	if !Register(mux, Deps{
		Store: &fakeStore{}, TokenSHA256: digest(secret), PublicURL: "https://codesamplex.dev",
		Version: "v1.2.3-test", StartedAt: now.Add(-26 * time.Hour),
		Now: func() time.Time { return now }, Farm: store, Authoring: store,
	}) {
		t.Fatal("valid token hash did not register /admin")
	}
	return mux, secret
}

// withholdOne drives a coordinate off the board the way the fleet would: two
// independent writers measuring that nothing callable exists.
func withholdOne(t *testing.T, store *serverstore.Fake, now time.Time) {
	t.Helper()
	ctx := t.Context()
	candidates := []serverstore.WantedRow{{
		Ecosystem: "maven", Name: "org.jetbrains.kotlin/kotlin-gradle-plugins-bom",
		Version: "2.2.20", Symbol: "", Kind: "EXPANSION",
	}}
	for _, session := range []string{"writer-a", "writer-b"} {
		if _, ok, err := store.ClaimAuthoringWork(ctx, session, candidates, now, now.Add(24*time.Hour)); err != nil || !ok {
			t.Fatalf("%s claim: ok=%v err=%v", session, ok, err)
		}
		if _, ok, err := store.ReportAuthoringOutcome(ctx, session,
			serverstore.AuthoringNoCallableSymbol, "pom-only artifact: no jar", now); err != nil || !ok {
			t.Fatalf("%s report: ok=%v err=%v", session, ok, err)
		}
	}
}

func TestOperatorSeesWhyAuthoringWorkWasWithheld(t *testing.T) {
	store := serverstore.NewFake()
	now := time.Date(2026, 8, 22, 17, 30, 0, 0, time.UTC)
	withholdOne(t, store, now)
	mux, secret := withheldMux(t, store, now)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/withheld-work", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Withheld []struct {
			Package       string `json:"package"`
			Symbol        string `json:"symbol"`
			Kind          string `json:"kind"`
			Reason        string `json:"reason"`
			Attempts      int    `json:"attempts"`
			NoOutput      int    `json:"noOutput"`
			Impossible    int    `json:"sessionsMeasuringImpossible"`
			QuarantinedAt string `json:"quarantinedAt"`
			AgeHours      float64
			ReopensAt     string `json:"reopensAt"`
			Permanent     bool   `json:"needsOperator"`
			History       []struct {
				Outcome string `json:"outcome"`
				Detail  string `json:"detail"`
			} `json:"history"`
		} `json:"withheld"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Withheld) != 1 {
		t.Fatalf("withheld = %d rows, want 1: %s", len(got.Withheld), rec.Body.String())
	}
	row := got.Withheld[0]
	if row.Package != "pkg:maven/org.jetbrains.kotlin/kotlin-gradle-plugins-bom@2.2.20" {
		t.Errorf("package = %q", row.Package)
	}
	if !strings.Contains(row.Reason, "no callable symbol") {
		t.Errorf("reason = %q, want the measured reason", row.Reason)
	}
	if row.Impossible != 2 {
		t.Errorf("sessionsMeasuringImpossible = %d, want 2", row.Impossible)
	}
	if row.QuarantinedAt == "" {
		t.Error("no age: an operator cannot tell a withholding from last hour from one from last month")
	}
	if !row.Permanent {
		t.Error("a structural withholding needs an operator, and the panel must say so")
	}
	if len(row.History) == 0 {
		t.Fatal("no evidence: the reason without the attempts behind it cannot be judged")
	}
	// The worker's own note is the evidence an operator reads first.
	found := false
	for _, entry := range row.History {
		if strings.Contains(entry.Detail, "pom-only") {
			found = true
		}
	}
	if !found {
		t.Errorf("history lost the writers' notes: %+v", row.History)
	}
}

func TestOperatorCanPutWithheldWorkBackFromThePanel(t *testing.T) {
	store := serverstore.NewFake()
	now := time.Date(2026, 8, 22, 17, 30, 0, 0, time.UTC)
	withholdOne(t, store, now)
	mux, secret := withheldMux(t, store, now)

	body := `{"ecosystem":"maven","name":"org.jetbrains.kotlin/kotlin-gradle-plugins-bom","version":"2.2.20","symbol":""}`
	req := httptest.NewRequest(http.MethodPost, "/admin/api/withheld-work/reopen", strings.NewReader(body))
	req.SetBasicAuth("recuerdame", secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://codesamplex.dev")
	req.Header.Set("X-CSX-CSRF", "1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var reopened struct {
		Reopened bool `json:"reopened"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reopened); err != nil || !reopened.Reopened {
		t.Fatalf("reopen = %+v err=%v", reopened, err)
	}
	rows, err := store.ListAuthoringQuarantine(t.Context(), now, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("still withheld after reopen: %d rows err=%v", len(rows), err)
	}
}

// A GET must never change state, and neither must an unauthenticated POST.
func TestReopeningWithheldWorkNeedsAnOperator(t *testing.T) {
	store := serverstore.NewFake()
	now := time.Date(2026, 8, 22, 17, 30, 0, 0, time.UTC)
	withholdOne(t, store, now)
	mux, _ := withheldMux(t, store, now)

	body := `{"ecosystem":"maven","name":"org.jetbrains.kotlin/kotlin-gradle-plugins-bom","version":"2.2.20","symbol":""}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/api/withheld-work/reopen", strings.NewReader(body)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// A logged-in operator's browser posting from another site is the case
	// the CSRF pair exists for: authenticated, and not the operator's doing.
	crossSite := httptest.NewRequest(http.MethodPost, "/admin/api/withheld-work/reopen", strings.NewReader(body))
	crossSite.SetBasicAuth("recuerdame", "a-long-random-admin-secret")
	crossSite.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, crossSite)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site status = %d, want 403", rec.Code)
	}

	rows, err := store.ListAuthoringQuarantine(t.Context(), now, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("an unauthorized caller changed the board: %d rows err=%v", len(rows), err)
	}
}

// The farm panel already answers "what is the network doing". How much of the
// board it is refusing to hand out belongs in the same answer.
func TestFarmPanelReportsWithheldCoordinates(t *testing.T) {
	store := serverstore.NewFake()
	now := time.Date(2026, 8, 22, 17, 30, 0, 0, time.UTC)
	withholdOne(t, store, now)
	mux, secret := withheldMux(t, store, now)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Health struct {
			WithheldCoordinates int `json:"withheldCoordinates"`
			WithheldByReason    []struct {
				Reason string `json:"reason"`
				Count  int    `json:"count"`
			} `json:"withheldByReason"`
		} `json:"health"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Health.WithheldCoordinates != 1 {
		t.Fatalf("withheldCoordinates = %d, want 1: %s", got.Health.WithheldCoordinates, rec.Body.String())
	}
	if len(got.Health.WithheldByReason) != 1 || got.Health.WithheldByReason[0].Count != 1 {
		t.Fatalf("withheldByReason = %+v", got.Health.WithheldByReason)
	}
}
