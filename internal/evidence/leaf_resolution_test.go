package evidence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/adapters/node"
	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Reproduces the Farm's successful npm checks leaving a leaf's DEPENDENCY
// lease open: the explicit lockfile fact must survive recording and batching.
func TestNpmLeafResolutionReachesServerFromOrdinaryRun(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "npm-ls", true: "npm-test"}[known], func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"package.json":      `{"dependencies":{"hono":"4.13.5"}}`,
				"package-lock.json": `{"lockfileVersion":3,"packages":{"":{"dependencies":{"hono":"4.13.5"}},"node_modules/hono":{"version":"4.13.5"}}}`,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			res, err := scanner.Scan(t.Context(), dir, []scanner.Adapter{node.Adapter{}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i := range res.Packages {
				res.Packages[i].Publicness = scanner.PublicnessPublic
			}
			res.Env = testEnvFP()
			db, ident, cfg := testDB(t), testIdentity(t), config.Default()
			rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
			profile := scanner.CommandProfile{}
			if known {
				profile = knownProfile()
			}
			if err := rec.RecordRun(t.Context(), dir, res, profile, 0, ""); err != nil {
				t.Fatal(err)
			}
			b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
			batches, err := b.Drain(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(batches) != 1 || !batches[0].DependsOnNone {
				t.Fatalf("ordinary resolved leaf lost explicit no-dependencies fact: %+v", batches)
			}
			f := serverstore.NewFake()
			accepted, rejected, err := f.IngestBatches(t.Context(), batches)
			if err != nil || accepted != 1 || len(rejected) != 0 {
				t.Fatalf("ingest: %d %+v %v", accepted, rejected, err)
			}
			none, err := f.DependencyProvenNone(t.Context(), "npm", "hono", "4.13.5")
			if err != nil || !none {
				t.Fatalf("dependency axis remains open: none=%v err=%v", none, err)
			}
		})
	}
}

func TestLeafFactsDoNotEscapePublicnessOrContradictPrivateEdges(t *testing.T) {
	res := fakeScanResult()
	for i := range res.Packages {
		res.Packages[i].DependsOnNone = true
	}
	res.Edges = []scanner.Edge{{Parent: res.Packages[0].PURL, Child: res.Packages[1].PURL}}
	db, ident, cfg := testDB(t), testIdentity(t), config.Default()
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	if err := rec.RecordRun(t.Context(), t.TempDir(), res, knownProfile(), 0, ""); err != nil {
		t.Fatal(err)
	}
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	batches, err := b.Drain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range batches {
		if batch.Package != res.Packages[0].PURL.String() || batch.DependsOnNone || len(batch.DependsOn) != 0 {
			t.Fatalf("private edge escaped or became a false leaf: %+v", batch)
		}
	}
	if len(batches) != 2 || batches[0].Result != domain.ResultPass {
		t.Fatalf("missing public observations: %+v", batches)
	}
}
