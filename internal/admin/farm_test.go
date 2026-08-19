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

func farmMux(t *testing.T, store serverstore.FarmStatsStore, instances []Instance) (*http.ServeMux, string) {
	t.Helper()
	secret := "a-long-random-admin-secret"
	now := time.Date(2026, 8, 19, 17, 30, 0, 0, time.UTC)
	mux := http.NewServeMux()
	if !Register(mux, Deps{
		Store: &fakeStore{}, TokenSHA256: digest(secret), PublicURL: "https://codesamplex.dev",
		Version: "v1.2.3-test", StartedAt: now.Add(-26 * time.Hour),
		Now: func() time.Time { return now }, Farm: store, Instances: instances,
	}) {
		t.Fatal("valid token hash did not register /admin")
	}
	return mux, secret
}

// The panel reports what is running, what it produced, and the two network
// numbers that had nowhere to appear: how much of the corpus is duplicated,
// and which operating systems the network can actually speak for.
func TestFarmPanelReportsWorkersHealthAndCost(t *testing.T) {
	store := serverstore.NewFake()
	ctx := t.Context()
	now := time.Date(2026, 8, 19, 17, 30, 0, 0, time.UTC)
	if err := store.IssueAuthoringSessions(ctx, []serverstore.AuthoringSessionRow{
		{TokenHash: "h-live", SessionID: "live", Label: "linux-slot1", Model: "agy",
			Reasoning: "auto", IssuedAt: now.Add(-2 * time.Hour), IdleExpiresAt: now.Add(time.Hour)},
		{TokenHash: "h-dead", SessionID: "dead", Label: "windows-slot1", Model: "agy",
			Reasoning: "auto", IssuedAt: now.Add(-25 * time.Minute), IdleExpiresAt: now.Add(35 * time.Minute)},
	}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshAuthoringSession(ctx, "h-live", "10.0.0.1", "csx-farm-linux-1",
		now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	mux, secret := farmMux(t, store, []Instance{
		{Name: "csx-prod-1", MonthlyUSD: 12},
		{Name: "csx-farm-linux-1", MonthlyUSD: 24},
		{Name: "csx-farm-windows-1", MonthlyUSD: 44},
	})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var got struct {
		Workers []struct {
			Label        string `json:"label"`
			ComputerName string `json:"computerName"`
			Started      bool   `json:"started"`
		} `json:"workers"`
		Health struct {
			PublicSamples   int            `json:"publicSamples"`
			DuplicateCoords int            `json:"duplicateCoordinates"`
			ReceiptsByOS    map[string]int `json:"receiptsByOs"`
		} `json:"health"`
		Instances []struct {
			Name       string  `json:"name"`
			MonthlyUSD float64 `json:"monthlyUsd"`
		} `json:"instances"`
		MonthlyTotalUSD float64 `json:"monthlyTotalUsd"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Workers) != 2 {
		t.Fatalf("workers = %+v, want 2", got.Workers)
	}
	byLabel := map[string]bool{}
	for _, w := range got.Workers {
		byLabel[w.Label] = w.Started
	}
	if !byLabel["linux-slot1"] {
		t.Error("the live worker is not reported as started")
	}
	if byLabel["windows-slot1"] {
		t.Error("a worker that never refreshed is reported as started")
	}
	if len(got.Instances) != 3 || got.MonthlyTotalUSD != 80 {
		t.Errorf("instances = %+v total = %v, want 3 and 80", got.Instances, got.MonthlyTotalUSD)
	}
}

// Without the store configured the panel says so rather than rendering zeros,
// because a zero duplicate rate and "no data" must never look the same.
func TestFarmPanelIsHonestWhenUnavailable(t *testing.T) {
	mux, secret := farmMux(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (body=%s)", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}
