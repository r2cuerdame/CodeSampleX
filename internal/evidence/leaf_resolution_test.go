package evidence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/adapters/goadapter"
	"github.com/r2cuerdame/codesamplex/adapters/node"
	"github.com/r2cuerdame/codesamplex/adapters/python"
	"github.com/r2cuerdame/codesamplex/adapters/rust"
	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestOtherDependencyObserversDeliverExplicitLeaves(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adapter scanner.Adapter
		purl    string
		files   map[string]string
	}{
		{"golang", goadapter.New(), "pkg:golang/github.com/google/go-cmp@v0.5.9", map[string]string{
			"go.mod":                      "module example.com/leaf\n\ngo 1.20\nrequire github.com/google/go-cmp v0.5.9\n",
			".csx-vendor/go-modules.json": "{\"Path\":\"github.com/google/go-cmp\",\"Version\":\"v0.5.9\"}\n",
			".csx-vendor/gomod/cache/download/github.com/google/go-cmp/@v/v0.5.9.mod": "module github.com/google/go-cmp\n\ngo 1.13\n",
		}},
		{"pypi", python.New(), "pkg:pypi/idna@3.10", map[string]string{
			"pyproject.toml": "[project]\nname = \"leaf\"\nversion = \"0.1.0\"\ndependencies = [\"idna==3.10\"]\n",
			"uv.lock":        "version = 1\nrevision = 3\nrequires-python = \">=3.9\"\n[[package]]\nname = \"idna\"\nversion = \"3.10\"\nsource = { registry = \"https://pypi.org/simple\" }\n",
		}},
		{"cargo", rust.New(), "pkg:cargo/itoa@1.0.15", map[string]string{
			"Cargo.toml": "[package]\nname = \"leaf\"\nversion = \"0.1.0\"\nedition = \"2021\"\n[dependencies]\nitoa = \"=1.0.15\"\n",
			"Cargo.lock": "version = 4\n[[package]]\nname = \"itoa\"\nversion = \"1.0.15\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\n",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			res, err := scanner.Scan(t.Context(), dir, []scanner.Adapter{tc.adapter}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i := range res.Packages {
				res.Packages[i].Publicness = scanner.PublicnessPublic
			}
			res.Env = domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: tc.name, OS: "linux", Arch: "amd64", Runtime: map[string]string{"golang": "go", "pypi": "python", "cargo": "rust"}[tc.name]}
			db, ident, cfg := testDB(t), testIdentity(t), config.Default()
			rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
			if err := rec.RecordRun(t.Context(), dir, res, scanner.CommandProfile{}, 0, ""); err != nil {
				t.Fatal(err)
			}
			b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
			batches, err := b.Drain(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, batch := range batches {
				if batch.Package == tc.purl && batch.DependsOnNone {
					f := serverstore.NewFake()
					accepted, rejected, err := f.IngestBatches(t.Context(), batches)
					if err != nil || accepted != len(batches) || len(rejected) != 0 {
						t.Fatalf("ingest: %d %+v %v", accepted, rejected, err)
					}
					p, err := domain.ParsePURL(tc.purl)
					if err != nil {
						t.Fatal(err)
					}
					none, err := f.DependencyProvenNone(t.Context(), p.Ecosystem, p.Name, p.Version)
					if err != nil || !none {
						t.Fatalf("dependency axis remains open: none=%v err=%v", none, err)
					}
					return
				}
			}
			t.Fatalf("resolved leaf %s cannot close Dependency through an ordinary run: %+v", tc.purl, batches)
		})
	}
}

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
