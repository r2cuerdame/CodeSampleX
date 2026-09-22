package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The CLI census on the farm panel is the plan the funnel offers, drawn from
// the same rows by the same rule: the seeds, the farm's version per OS, and
// what this farm cannot reach, by reason. Absent when the store cannot
// answer, never zeros.
func TestFarmPanelReportsTheCLICensus(t *testing.T) {
	store := serverstore.NewFake()
	code := 0
	batches := []domain.ObservationBatch{{
		SchemaVersion: 2, Epoch: "2026-08-19", AnonID: "0123456789abcdef0123456789abcdef",
		ProjectBucket: "0123456789abcdef0123456789abcdef",
		Package:       "pkg:generic/cli/git@2.47.2", Symbol: "farm:--version",
		Environment:   domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "linux", Arch: "amd64"},
		Stage:         domain.StageProjectProcess, Result: domain.ResultPass, ObservationCount: 1,
		TerminationKind: domain.TerminationExit, ExitCode: &code,
	}, {
		SchemaVersion: 2, Epoch: "2026-08-19", AnonID: "0123456789abcdef0123456789abcdef",
		ProjectBucket: "0123456789abcdef0123456789abcdef",
		Package:       "pkg:generic/cli/gh@2.78.0", Symbol: "field:pr view <arg>",
		Environment:   domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "darwin", Arch: "arm64"},
		Stage:         domain.StageProjectProcess, Result: domain.ResultPass, ObservationCount: 3,
		TerminationKind: domain.TerminationExit, ExitCode: &code,
	}}
	if accepted, rejected, err := store.IngestBatches(t.Context(), batches); err != nil || accepted != 2 {
		t.Fatalf("ingest accepted=%d rejected=%+v err=%v", accepted, rejected, err)
	}
	secret := "a-long-random-admin-secret"
	now := time.Date(2026, 8, 19, 17, 30, 0, 0, time.UTC)
	mux := http.NewServeMux()
	if !Register(mux, Deps{
		Store: &fakeStore{}, TokenSHA256: digest(secret), PublicURL: "https://codesamplex.dev",
		Version: "v1.2.3-test", StartedAt: now.Add(-26 * time.Hour),
		Now: func() time.Time { return now }, Farm: store, CLICoverage: store,
	}) {
		t.Fatal("valid token hash did not register /admin")
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		CLI *struct {
			FarmOS         []string `json:"farmOS"`
			Observed       int      `json:"observed"`
			FarmObserved   int      `json:"farmObserved"`
			Queued         int      `json:"queued"`
			Unavailable    int      `json:"unavailable"`
			Unavailability []struct {
				Reason string `json:"reason"`
				Count  int    `json:"count"`
			} `json:"unavailability"`
			Tools []struct {
				Tool         string            `json:"tool"`
				Seed         bool              `json:"seed"`
				Queued       int               `json:"queued"`
				FarmVersions map[string]string `json:"farmVersions"`
			} `json:"tools"`
		} `json:"cli"`
		CLIAt string `json:"cliAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CLI == nil || body.CLIAt == "" {
		t.Fatalf("no CLI census: %s", rec.Body.String())
	}
	c := body.CLI
	if strings.Join(c.FarmOS, ",") != "linux,windows" || c.Observed != 2 || c.FarmObserved != 1 || c.Queued == 0 {
		t.Fatalf("census = %+v", c)
	}
	if c.Unavailable != 1 || len(c.Unavailability) != 1 || c.Unavailability[0].Reason != "no farm lane for darwin" {
		t.Fatalf("unavailability = %+v", c.Unavailability)
	}
	if len(c.Tools) == 0 || c.Tools[0].Tool != "gh" || !c.Tools[0].Seed {
		t.Fatalf("tools do not lead with the seeds: %+v", c.Tools)
	}
	for _, tool := range c.Tools {
		if tool.Tool == "git" && tool.FarmVersions["linux"] != "2.47.2" {
			t.Fatalf("git farm version = %+v", tool.FarmVersions)
		}
	}
}

// Without a CLI store the section is absent, not zero.
func TestFarmPanelOmitsTheCLICensusWithoutAStore(t *testing.T) {
	mux, secret := farmMux(t, serverstore.NewFake(), nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["cli"] != nil {
		t.Fatalf("cli = %v, want null", body["cli"])
	}
}

// The panel and the payload have to agree on the keys.
func TestCLIPanelReadsTheKeysTheServerSends(t *testing.T) {
	js, err := adminStaticFS.ReadFile("static/admin.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, key := range []string{"data.cli", "farmObserved", "unavailability", "farmVersions", "data.cliAt"} {
		if !strings.Contains(src, key) {
			t.Errorf("admin.js never reads %s; farm_http.go sends it", key)
		}
	}
	html, err := templateFS.ReadFile("templates/admin.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `id="farm-cli"`) {
		t.Error(`the farm tab has no #farm-cli container for the script to fill`)
	}
}
