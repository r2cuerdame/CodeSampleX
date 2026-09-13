package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/adapters/node"
	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// A production-shaped leaf assignment must advance through the real local
// recorder and cache into a Sample claim, private draft and signed promotion.
// This verifies pipeline boundaries, not execution of Hono's package contract.
func TestAuthoringPipelineFromDependencyObservationToPublishedDraft(t *testing.T) {
	const purl = "pkg:npm/hono@4.13.5"
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	store := newSnapshotStore(
		serverstore.WantedRow{Ecosystem: "npm", Name: "hono", Version: "4.13.5", Kind: "DEPENDENCY", Axis: serverstore.AuthoringAxisDependency, Score: 48411},
		serverstore.WantedRow{Ecosystem: "npm", Name: "hono", Version: "4.13.5", Kind: "EXPANSION", Axis: serverstore.AuthoringAxisEvidence},
		serverstore.WantedRow{Ecosystem: "npm", Name: "hono", Version: "4.13.5", Kind: "EXPANSION", Axis: serverstore.AuthoringAxisSample, Score: 48411, TargetOS: "windows"},
	)
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Store = store; d.Cfg.Publishing = "seeded" })
	authoringSession(t, store.Fake, token, "pipeline-author", testNow)
	if err := store.UpsertPackage(t.Context(), serverstore.PackageRow{PURL: purl, Ecosystem: "npm", Name: "hono", Version: "4.13.5", Publicness: "PUBLIC", LastSeen: testNow}); err != nil {
		t.Fatal(err)
	}
	assertAxis := func(want string) {
		t.Helper()
		result := requestAuthoringWork(t, srv.URL, token)
		work, _ := result["work"].(map[string]any)
		if result["status"] != "ASSIGNED" || work["axis"] != want || work["package"] != purl {
			t.Fatalf("want %s claim: %#v", want, result)
		}
	}
	assertAxis(serverstore.AuthoringAxisDependency)
	// A repeated agent poll/heartbeat must keep the same deliverable.
	assertAxis(serverstore.AuthoringAxisDependency)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"package.json":      `{"dependencies":{"hono":"4.13.5"}}`,
		"package-lock.json": `{"lockfileVersion":3,"packages":{"":{"dependencies":{"hono":"4.13.5"}},"node_modules/hono":{"version":"4.13.5"}}}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := scanner.Scan(t.Context(), dir, []scanner.Adapter{node.Adapter{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range observed.Packages {
		observed.Packages[i].Publicness = scanner.PublicnessPublic
	}
	observed.Env = nodeEnv("esm")
	observed.Env.OS = "linux"
	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	recorder := evidence.Recorder{DB: db, Ident: ident, Cfg: cfg}
	if err := recorder.RecordCommandOutput(t.Context(), dir, observed, scanner.CommandProfile{}, []string{"npm", "ls"}, 0, evidence.CommandOutput{ToolVersion: "10.9.8", Shell: "bash"}); err != nil {
		t.Fatal(err)
	}
	batcher := evidence.Batcher{DB: db, Ident: ident, Cfg: cfg}
	batches, err := batcher.Drain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	leafFound := false
	for _, batch := range batches {
		if batch.Package == purl && batch.DependsOnNone {
			leafFound = true
		}
	}
	if len(batches) != 2 || !leafFound {
		t.Fatalf("explicit leaf lost before upload: %+v", batches)
	}
	var uploaded ingestResponse
	response := postJSON(t, srv.URL+"/v1/evidence/batches", map[string]any{"batches": batches}, &uploaded)
	if response.StatusCode != http.StatusAccepted || uploaded.Accepted != 2 || len(uploaded.Rejected) != 0 {
		t.Fatalf("upload status=%d result=%+v", response.StatusCode, uploaded)
	}
	assertAxis(serverstore.AuthoringAxisSample)
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("completion needed a corpus refresh: scans=%d", got)
	}
	manifest := testManifest()
	manifest.Packages = []string{purl}
	manifest.Case.Packages = []string{purl}
	manifest.Symbols = []string{"Hono.fetch"}
	manifest.Case.Goal = "serve a Hono response"
	manifest.Case.Contract = []string{"returns the configured response"}
	submitAndCrossVerifyAuthoringDraft(t, srv.URL, store.Fake, token, manifest)
	if result := requestAuthoringWork(t, srv.URL, token); result["status"] != "NO_WORK" {
		t.Fatalf("completed pipeline was reissued: %#v", result)
	}
}
