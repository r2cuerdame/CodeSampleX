package verifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/adapters"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// What npm actually writes: every entry carries the registry artifact it came
// from, and resolvedPackages checks that URL before believing a version. A
// fixture without it reports nothing resolved, which is not what production
// looks like.
const treeLock = `{
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "sample", "version": "1.0.0", "dependencies": {"express": "^4.18.2"}},
    "node_modules/express": {
      "version": "4.18.2",
      "resolved": "https://registry.npmjs.org/express/-/express-4.18.2.tgz",
      "dependencies": {"body-parser": "1.20.1"}
    },
    "node_modules/body-parser": {
      "version": "1.20.1",
      "resolved": "https://registry.npmjs.org/body-parser/-/body-parser-1.20.1.tgz"
    }
  }
}`

func treeWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(treeLock), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"sample"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	installNPM(t, dir, "express", "4.18.2")
	installNPM(t, dir, "body-parser", "1.20.1")
	return dir
}

// installNPM writes the on-disk half of a resolution. resolvedPackages
// deliberately refuses a lockfile entry the installed tree does not agree
// with, so a workspace with only a lockfile is a workspace where nothing
// resolved — which is not what a verification looks like.
func installNPM(t *testing.T, dir, name, version string) {
	t.Helper()
	pkgDir := filepath.Join(dir, "node_modules", filepath.FromSlash(name))
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"` + name + `","version":"` + version + `"}`
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func treeManifest() domain.SampleManifest {
	var m domain.SampleManifest
	m.Environment.Ecosystem = "npm"
	m.Packages = []string{"pkg:npm/express@4.18.2"}
	return m
}

func treeReceipt(resolve string) domain.VerificationReceipt {
	r := domain.VerificationReceipt{
		SampleID: "sha256:" + "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"[:64],
		PeerID:   "peer-abc",
		Stages:   map[string]string{"resolve": resolve},
	}
	// Whatever environment.Collect produces, because that is what a receipt
	// actually carries. A fixture missing the schema version is how the
	// backfill's refusals stayed invisible until production.
	r.Environment.SchemaVersion = 1
	r.Environment.Ecosystem = "npm"
	r.Environment.OS = "linux"
	r.Environment.Arch = "amd64"
	return r
}

// treeServer captures what a verification posted to the evidence endpoint.
func treeServer(t *testing.T, got *[]domain.ObservationBatch) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/evidence/batches" {
			t.Errorf("posted to %s, want the evidence endpoint", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Batches []domain.ObservationBatch `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		*got = append(*got, body.Batches...)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":1,"rejected":[]}`))
	}))
}

// Every verification resolves a real lockfile in a container, and that file is
// the only place the dependency tree is ever written down. It was deleted with
// the workspace: the network knew a sample for 1,766 of 3,138 public
// coordinates and a dependency tree for 563, because edges arrived from one
// source only — people running `csx run` on their own projects.
//
// The receipt cannot carry it. SigningBytes canonicalises the whole struct, so
// a peer on an older build would decode a receipt with a new field, drop it,
// recompute different signing bytes and reject a receipt that is perfectly
// valid. The tree therefore leaves by the wire path that already exists for
// edges, which the server already turns into dependency_edge rows.
func TestAVerificationReportsTheTreeItResolved(t *testing.T) {
	var got []domain.ObservationBatch
	srv := treeServer(t, &got)
	defer srv.Close()

	cv := &CrossVerifier{ServerURL: srv.URL, HTTP: srv.Client()}
	r := treeReceipt(sandbox.ResultPass)
	cv.reportResolvedTree(context.Background(), treeWorkspace(t), treeManifest(), r)

	if len(got) != 1 {
		t.Fatalf("posted %d batches, want the one parent the lockfile records: %+v", len(got), got)
	}
	b := got[0]
	if b.Package != "pkg:npm/express@4.18.2" {
		t.Errorf("package = %q", b.Package)
	}
	if len(b.DependsOn) != 1 || b.DependsOn[0] != "pkg:npm/body-parser@1.20.1" {
		t.Errorf("dependsOn = %v, want the edge the lockfile records", b.DependsOn)
	}
	// The server derives observations from this same receipt and buckets them
	// by the sample id. Landing the edges anywhere else would count one
	// verification as two projects, and a count of projects is the whole
	// meaning of the number these edges feed.
	if want := domain.SampleProjectBucket(r.SampleID); b.ProjectBucket != want {
		t.Errorf("projectBucket = %q, want the receipt's own %q", b.ProjectBucket, want)
	}
	if b.AnonID != r.PeerID {
		t.Errorf("anonId = %q, want the receipt's peer %q", b.AnonID, r.PeerID)
	}
	// The sample declared express; body-parser arrived through it.
	if !b.Direct {
		t.Error("a package the sample declared was reported as somebody else's transitive dependency")
	}
	if b.Stage != domain.StageUsed {
		t.Errorf("stage = %q; the tree says what was installed, not what was built", b.Stage)
	}
}

// A resolve that failed left no lockfile, or a partial one, and a tree read out
// of that would name dependencies at versions nothing installed.
func TestAFailedResolveReportsNoTree(t *testing.T) {
	var got []domain.ObservationBatch
	srv := treeServer(t, &got)
	defer srv.Close()

	cv := &CrossVerifier{ServerURL: srv.URL, HTTP: srv.Client()}
	cv.reportResolvedTree(context.Background(), treeWorkspace(t), treeManifest(), treeReceipt(sandbox.ResultFail))

	if len(got) != 0 {
		t.Errorf("a failed resolve reported %d batches: %+v", len(got), got)
	}
}

// One verification runs one resolver. A lockfile for another ecosystem may
// merely be shipped beside the sample, and reading it would turn an npm PASS
// into a Cargo claim that nothing ran.
func TestOnlyTheResolverThatRanReportsATree(t *testing.T) {
	var got []domain.ObservationBatch
	srv := treeServer(t, &got)
	defer srv.Close()

	m := treeManifest()
	m.Environment.Ecosystem = "cargo"
	m.Packages = []string{"pkg:cargo/serde@1.0.0"}

	cv := &CrossVerifier{ServerURL: srv.URL, HTTP: srv.Client()}
	cv.reportResolvedTree(context.Background(), treeWorkspace(t), m, treeReceipt(sandbox.ResultPass))

	if len(got) != 0 {
		t.Errorf("an npm lockfile beside a cargo sample reported %d batches: %+v", len(got), got)
	}
}

// A workspace with no lockfile resolved nothing that can be read, and saying
// nothing is the answer. Posting an empty batch would be a claim.
func TestNoLockfileReportsNothing(t *testing.T) {
	var got []domain.ObservationBatch
	srv := treeServer(t, &got)
	defer srv.Close()

	cv := &CrossVerifier{ServerURL: srv.URL, HTTP: srv.Client()}
	cv.reportResolvedTree(context.Background(), t.TempDir(), treeManifest(), treeReceipt(sandbox.ResultPass))

	if len(got) != 0 {
		t.Errorf("an empty workspace reported %d batches", len(got))
	}
}

// Every batch a verification sends must survive the server's own validation.
//
// This is the check the receipt backfill did not have. That run produced 9,883
// observations and the store refused every one of them, because the project
// bucket carried its "sha256:" prefix and was 71 bytes against a 64-byte
// limit — a shape no unit test had exercised, since the fixtures used short
// ids. The run then reported the refusals as a bare count and looked like it
// had worked.
//
// So the batches go through ValidateBatch here, built from a real 64-hex
// sample id, rather than being trusted because the struct looks right.
func TestEveryTreeBatchSurvivesTheServersValidation(t *testing.T) {
	r := treeReceipt(sandbox.ResultPass)
	edges := ResolvedEdges(context.Background(), treeWorkspace(t), treeManifest(), adapters.All())
	batches := TreeBatches(edges, resolvedPackages(treeWorkspace(t), treeManifest()), treeManifest(), r, "2026-08-30")
	if len(batches) == 0 {
		t.Fatal("no batches to validate")
	}
	for _, b := range batches {
		if err := serverstore.ValidateBatch(b); err != nil {
			t.Errorf("the server would refuse this batch: %v\n%+v", err, b)
		}
	}
}

// And the server must turn them into the edges they carry. A batch that
// validates but contributes no dependency_edge row would fill nothing.
func TestTheServerWritesTheEdgesAVerificationSends(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	r := treeReceipt(sandbox.ResultPass)
	edges := ResolvedEdges(ctx, treeWorkspace(t), treeManifest(), adapters.All())

	accepted, rejected, err := store.IngestBatches(ctx, TreeBatches(edges, resolvedPackages(treeWorkspace(t), treeManifest()), treeManifest(), r, "2026-08-30"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 {
		t.Fatalf("the store refused %d of them: %+v", len(rejected), rejected)
	}
	if accepted == 0 {
		t.Fatal("nothing was accepted")
	}

	// Dependencies asks what a package depends ON, so the parent is the key.
	got, err := store.Dependencies(ctx, "npm", "express")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("the verification's tree produced no dependency edge — the axis it exists to fill stays empty")
	}
	if got[0].ParentName != "express" || got[0].ChildName != "body-parser" {
		t.Errorf("edge = %s -> %s, want express -> body-parser", got[0].ParentName, got[0].ChildName)
	}
}

// A fingerprint whose shape we cannot vouch for produces nothing, rather than
// batches the server will refuse one at a time.
func TestAnUnversionedEnvironmentSendsNoTree(t *testing.T) {
	r := treeReceipt(sandbox.ResultPass)
	r.Environment.SchemaVersion = 0
	edges := ResolvedEdges(context.Background(), treeWorkspace(t), treeManifest(), adapters.All())
	if got := TreeBatches(edges, resolvedPackages(treeWorkspace(t), treeManifest()), treeManifest(), r, "2026-08-30"); len(got) != 0 {
		t.Errorf("built %d batches the server would refuse", len(got))
	}
}

// A sample whose package genuinely declares no dependencies must say so.
//
// Reporting nothing leaves the coordinate indistinguishable from one nobody
// resolved, and the dependency axis answers a release only when it appears as
// a PARENT of an edge — which a leaf never is. Measured on production: 490
// coordinates appear as a child of a resolved tree and never as a parent, a
// quarter of everything open on that axis, and no amount of farm work could
// close them.
func TestASampleWhosePackageHasNoDependenciesSaysSo(t *testing.T) {
	dir := t.TempDir()
	// left-pad is in the lockfile with its own entry and no dependencies.
	lock := `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"name": "sample", "version": "1.0.0", "dependencies": {"left-pad": "^1.3.0"}},
	    "node_modules/left-pad": {
	      "version": "1.3.0",
	      "resolved": "https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz"
	    }
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"sample"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	installNPM(t, dir, "left-pad", "1.3.0")

	var m domain.SampleManifest
	m.Environment.Ecosystem = "npm"
	m.Packages = []string{"pkg:npm/left-pad@1.3.0"}

	var got []domain.ObservationBatch
	srv := treeServer(t, &got)
	defer srv.Close()

	cv := &CrossVerifier{ServerURL: srv.URL, HTTP: srv.Client()}
	cv.reportResolvedTree(context.Background(), dir, m, treeReceipt(sandbox.ResultPass))

	if len(got) != 1 {
		t.Fatalf("posted %d batches, want one saying the package declares nothing: %+v", len(got), got)
	}
	b := got[0]
	if b.Package != "pkg:npm/left-pad@1.3.0" {
		t.Errorf("package = %q", b.Package)
	}
	if !b.DependsOnNone {
		t.Error("the batch does not say the resolution found no dependencies, so the coordinate stays an open gap")
	}
	if len(b.DependsOn) != 0 {
		t.Errorf("dependsOn = %v, want empty", b.DependsOn)
	}
}

// Silence is not the same claim. A package the resolver never placed in the
// lockfile must produce nothing at all rather than a claim that it has no
// dependencies.
func TestAPackageTheResolverNeverPlacedClaimsNothing(t *testing.T) {
	var got []domain.ObservationBatch
	srv := treeServer(t, &got)
	defer srv.Close()

	m := treeManifest()
	m.Packages = []string{"pkg:npm/never-installed@9.9.9"}

	cv := &CrossVerifier{ServerURL: srv.URL, HTTP: srv.Client()}
	cv.reportResolvedTree(context.Background(), treeWorkspace(t), m, treeReceipt(sandbox.ResultPass))

	for _, b := range got {
		if b.Package == "pkg:npm/never-installed@9.9.9" {
			t.Errorf("a package the lockfile never contained was reported: %+v", b)
		}
	}
}
