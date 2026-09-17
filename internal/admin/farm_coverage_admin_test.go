package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// alwaysFailsLiveCoverageJoin embeds a real Fake so every FarmStatsStore
// method except the retired live corpus join behaves normally, and makes
// that one call panic. If the admin handler ever fell back to it (the
// pre-CSX-452 behavior), this test would panic or hang instead of answering
// -- FarmStatsStore no longer even declares FarmCoverage, so this is a
// belt-and-suspenders runtime proof alongside that compile-time one.
type alwaysFailsLiveCoverageJoin struct {
	*serverstore.Fake
}

func (s *alwaysFailsLiveCoverageJoin) FarmCoverage(context.Context) ([]serverstore.FarmAxisCoverage, error) {
	panic("admin must not call the live FarmCoverage join -- it reads the Builder-materialized farm_coverage table instead (CSX-452)")
}

// The admin coverage panel reads farm_coverage, the table the Builder
// publishes each pass (CSX-452), not the live corpus-wide join that used to
// run on every admin cache-miss. A store whose live join would panic or
// time out must still answer, because the handler never calls it.
func TestFarmPanelCoverageReadsMaterializedTableNotLiveJoin(t *testing.T) {
	store := &alwaysFailsLiveCoverageJoin{Fake: serverstore.NewFake()}
	ctx := context.Background()
	generatedAt := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	rows := []serverstore.FarmAxisCoverage{
		{OS: "linux", Ecosystem: "npm", Observed: 40, Measured: 30, Proven: 25, ObservedProven: 20},
	}
	if err := store.PutFarmCoverage(ctx, rows, generatedAt); err != nil {
		t.Fatalf("PutFarmCoverage: %v", err)
	}

	mux, secret := farmMux(t, store, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Coverage []struct {
			OS             string `json:"os"`
			Ecosystem      string `json:"ecosystem"`
			Observed       int    `json:"observed"`
			Proven         int    `json:"proven"`
			ObservedProven int    `json:"observedProven"`
		} `json:"coverage"`
		CoverageAt          string `json:"coverageAt"`
		CoverageGeneratedAt string `json:"coverageGeneratedAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Coverage) != 1 || payload.Coverage[0].OS != "linux" ||
		payload.Coverage[0].Ecosystem != "npm" || payload.Coverage[0].Observed != 40 ||
		payload.Coverage[0].Proven != 25 || payload.Coverage[0].ObservedProven != 20 {
		t.Fatalf("coverage = %+v, want the published read model row", payload.Coverage)
	}
	if want := generatedAt.UTC().Format(time.RFC3339); payload.CoverageGeneratedAt != want {
		t.Fatalf("coverageGeneratedAt = %q, want %q", payload.CoverageGeneratedAt, want)
	}
	if payload.CoverageAt == "" {
		t.Fatal("coverageAt empty after a successful read")
	}
}

// A fresh install with no Builder pass yet must render coverage as
// "not yet computed" -- an empty rows list with no generatedAt -- rather
// than erroring or looking identical to a computed-but-empty corpus.
func TestFarmPanelCoverageNotYetComputedHasNoGeneratedAt(t *testing.T) {
	store := serverstore.NewFake()
	mux, secret := farmMux(t, store, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Coverage            []json.RawMessage `json:"coverage"`
		CoverageAt          string            `json:"coverageAt"`
		CoverageGeneratedAt string            `json:"coverageGeneratedAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Coverage) != 0 {
		t.Fatalf("coverage = %s, want empty before any Builder pass has published", payload.Coverage)
	}
	if payload.CoverageGeneratedAt != "" {
		t.Fatalf("coverageGeneratedAt = %q, want empty before any Builder pass has published", payload.CoverageGeneratedAt)
	}
}
