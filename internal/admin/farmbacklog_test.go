package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// The panel has to carry the grid at the grain a reader sees it, with the two
// dashes apart.
//
// coverageHoles counts RELEASES. In production that reads 99% covered while
// the package pages are mostly dashes, because a page spreads symbol against
// version and one proven release can draw forty cells and fill one. An
// operator reading only the release figure would conclude the fleet was
// nearly done with a grid that is 14% observed.
func TestFarmPanelReportsTheGridAtCellGrain(t *testing.T) {
	store := serverstore.NewFake()
	ctx := t.Context()

	// One release measured at symbol grain and one measured only at package
	// grain: the second is where the plain dashes come from, because its
	// column is drawn and nothing was ever recorded in it.
	cells := []struct {
		version, symbol string
		observations    int
		verifications   int
	}{
		{"7.7.1", "", 4, 1},
		{"7.7.1", "semver.clean", 0, 1}, // linked dash: our sample, no usage
		{"7.7.1", "semver.diff", 6, 1},  // observed
		{"6.3.1", "", 122, 0},           // release with no symbol row: plain dashes
	}
	for _, cell := range cells {
		purl := "pkg:npm/semver@" + cell.version
		if err := store.UpsertPackage(ctx, serverstore.PackageRow{
			PURL: purl, Ecosystem: "npm", Name: "semver",
			Version: cell.version, Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
		doc := `{"schemaVersion":1,"purl":"` + purl + `","symbol":"` + cell.symbol + `","rows":[{"observationClassCounts":{`
		if cell.observations > 0 {
			doc += `"USAGE_OBSERVATION":` + strconv.Itoa(cell.observations)
		}
		doc += `},"verificationCounts":{`
		if cell.verifications > 0 {
			doc += `"SAMPLE_VERIFICATION":` + strconv.Itoa(cell.verifications) + `,"distinctVerifyingPeers":1`
		}
		doc += `}}]}`
		if err := store.PutSnapshot(ctx, purl, cell.symbol, doc); err != nil {
			t.Fatal(err)
		}
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
			MatrixCells struct {
				Cells                     int `json:"cells"`
				Observed                  int `json:"observed"`
				VerifiedNoObservation     int `json:"verifiedNoObservation"`
				Unmeasured                int `json:"unmeasured"`
				PackagesShowingBothDashes int `json:"packagesShowingBothDashes"`
			} `json:"matrixCells"`
		} `json:"backlog"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	got := payload.Backlog.MatrixCells
	// Two releases × two symbols = four cells. One observed, one linked dash,
	// two plain dashes in the 6.3.1 column.
	if got.Cells != 4 || got.Observed != 1 || got.VerifiedNoObservation != 1 || got.Unmeasured != 2 {
		t.Errorf("matrixCells = %+v, want 4 cells / 1 observed / 1 verified-only / 2 unmeasured", got)
	}
	// The page shows both dash forms at once, which is the state R2C-89
	// reproduces from two live URLs.
	if got.PackagesShowingBothDashes != 1 {
		t.Errorf("packagesShowingBothDashes = %d, want 1", got.PackagesShowingBothDashes)
	}
}
