package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The panel could say how much had been proven and nothing at all about how
// much was left. With a queue that generates its own work that is the
// difference between "busy" and "nearly done", and the two looked identical.
func TestFarmPanelReportsWhatIsLeftAndHowFastItMoves(t *testing.T) {
	store := serverstore.NewFake()
	ctx := t.Context()
	now := time.Date(2026, 8, 19, 17, 30, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }

	// One chosen package whose lockfile resolved onto a release nobody has
	// ever reported: a hole the network can see, and a dependency it cannot.
	if _, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-19", AnonID: "peer", ProjectBucket: "proj",
		Package: "pkg:npm/express@5.1.0", Direct: true,
		DependsOn: []string{"pkg:npm/body-parser@2.2.0"},
		Stage:     domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 4,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22.18", ModuleSystem: "esm",
		},
	}}); err != nil || len(rejected) != 0 {
		t.Fatalf("ingest: rejected=%v err=%v", rejected, err)
	}
	if err := store.UpsertPackage(ctx, serverstore.PackageRow{
		PURL: "pkg:npm/express@5.1.0", Ecosystem: "npm", Name: "express",
		Version: "5.1.0", Major: "5", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
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
		Backlog struct {
			CoverageHoles       int            `json:"coverageHoles"`
			Dependencies        int            `json:"dependencies"`
			WindowSeconds       int            `json:"windowSeconds"`
			HandedOutInWindow   int            `json:"handedOutInWindow"`
			HandedOutByKind     map[string]int `json:"handedOutByKind"`
			FirstProvenInWindow int            `json:"firstProvenInWindow"`
		} `json:"backlog"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Backlog.CoverageHoles != 1 {
		t.Errorf("coverage holes = %d, want 1 (express)", payload.Backlog.CoverageHoles)
	}
	if payload.Backlog.Dependencies != 1 {
		t.Errorf("dependency backlog = %d, want 1 (body-parser)", payload.Backlog.Dependencies)
	}
	// A rate without its period reads as a total. The window is on the wire.
	if payload.Backlog.WindowSeconds != int(farmWindow/time.Second) {
		t.Errorf("windowSeconds = %d, want %d", payload.Backlog.WindowSeconds, int(farmWindow/time.Second))
	}
	if payload.Backlog.HandedOutInWindow != 0 || payload.Backlog.FirstProvenInWindow != 0 {
		t.Errorf("nothing has been handed out or proven yet: %+v", payload.Backlog)
	}
}

// The panel must fail loudly rather than render zeros. "Nothing measured" and
// "nothing left" are the two readings this whole section exists to keep apart.
func TestFarmPanelRefusesToRenderABacklogItCouldNotRead(t *testing.T) {
	mux, secret := farmMux(t, brokenBacklogStore{serverstore.NewFake()}, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when the backlog cannot be read", rec.Code)
	}
}

type brokenBacklogStore struct{ *serverstore.Fake }

func (brokenBacklogStore) FarmBacklogNow(_ context.Context, _, _ time.Time) (serverstore.FarmBacklog, error) {
	return serverstore.FarmBacklog{}, errBacklogUnavailable
}

var errBacklogUnavailable = errors.New("backlog unavailable")
