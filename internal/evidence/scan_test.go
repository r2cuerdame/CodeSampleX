package evidence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// writeFixtureNpmProject builds a small npm project: axios + electron
// (registry deps) and one file: dep that must classify as PRIVATE.
func writeFixtureNpmProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"package.json": `{
  "name": "fixture-proj",
  "dependencies": {"axios": "^1.12.0", "electron": "^38.0.0", "privlib": "file:../privlib"}
}`,
		"package-lock.json": `{
  "name": "fixture-proj",
  "lockfileVersion": 3,
  "packages": {
    "": {"dependencies": {"axios": "^1.12.0", "electron": "^38.0.0", "privlib": "file:../privlib"}},
    "node_modules/axios": {"version": "1.12.0", "resolved": "https://registry.npmjs.org/axios/-/axios-1.12.0.tgz"},
    "node_modules/electron": {"version": "38.0.0", "resolved": "https://registry.npmjs.org/electron/-/electron-38.0.0.tgz"},
    "node_modules/privlib": {"version": "1.0.0", "resolved": "file:../privlib"}
  }
}`,
		"index.js": "const axios = require('axios');\naxios.post('/x', {});\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// fakeNpmRegistry serves 200 for the given names, 404 otherwise.
func fakeNpmRegistry(t *testing.T, public ...string) (*registry.Checker, *httptest.Server) {
	t.Helper()
	known := map[string]bool{}
	for _, name := range public {
		known["/"+name] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if known[r.URL.Path] {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return &registry.Checker{HTTP: srv.Client(), BaseURLs: map[string]string{"npm": srv.URL}}, srv
}

func publicnessOf(res *scanner.ScanResult) map[string]string {
	out := map[string]string{}
	for _, p := range res.Packages {
		out[p.PURL.Name] = p.Publicness
	}
	return out
}

func TestScanFixtureNpmProject(t *testing.T) {
	dir := writeFixtureNpmProject(t)
	checker, _ := fakeNpmRegistry(t, "axios", "electron")

	res, err := Scan(context.Background(), dir, checker)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Adapters) == 0 {
		t.Fatal("no adapters detected for an npm project")
	}

	pub := publicnessOf(res)
	if pub["axios"] != scanner.PublicnessPublic {
		t.Errorf("axios publicness = %q, want PUBLIC", pub["axios"])
	}
	if pub["electron"] != scanner.PublicnessPublic {
		t.Errorf("electron publicness = %q, want PUBLIC", pub["electron"])
	}
	if pub["privlib"] != scanner.PublicnessPrivate {
		t.Errorf("privlib publicness = %q, want PRIVATE (file: dep)", pub["privlib"])
	}

	var sawAxiosPost bool
	for _, s := range res.Symbols {
		if s.Package.Name == "axios" && s.Family == "axios.post" {
			sawAxiosPost = true
			if s.Confidence != domain.SymbolProbable {
				t.Errorf("axios.post confidence = %q, want PROBABLE", s.Confidence)
			}
		}
	}
	if !sawAxiosPost {
		t.Errorf("axios.post not found in symbols: %+v", res.Symbols)
	}

	if res.Env.Ecosystem != "npm" {
		t.Errorf("env ecosystem = %q, want npm", res.Env.Ecosystem)
	}
	if res.Env.ModuleSystem != "cjs" {
		t.Errorf("env moduleSystem = %q, want cjs", res.Env.ModuleSystem)
	}

	// electron dependency ⇒ TARGET context hint, never the observation env
	// (docs/execution-context.md §3: builds still execute in node).
	if res.TargetContext != "electron" {
		t.Errorf("targetContext = %q, want electron", res.TargetContext)
	}
	if res.Env.ExecutionContext == "electron" {
		t.Error("target context leaked into the environment fingerprint")
	}

	if p := res.Classify([]string{"node", "index.js"}); !p.Known || p.Stage != domain.StageProjectProcess {
		t.Errorf("node command classified as %+v", p)
	}
	if p := res.Classify([]string{"frobnicate-xyz"}); p.Known {
		t.Errorf("nonsense command classified as known: %+v", p)
	}
}

func TestScanNilCheckerLeavesUnknown(t *testing.T) {
	dir := writeFixtureNpmProject(t)
	res, err := Scan(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	pub := publicnessOf(res)
	if pub["axios"] != scanner.PublicnessUnknown {
		t.Errorf("axios publicness without checker = %q, want UNKNOWN", pub["axios"])
	}
	if pub["privlib"] != scanner.PublicnessPrivate {
		t.Errorf("privlib publicness = %q, want PRIVATE", pub["privlib"])
	}
}

func TestScanDetectsBunAndDenoTargetContexts(t *testing.T) {
	bunDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bunDir, "bun.lock"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Scan(context.Background(), bunDir, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.TargetContext != "bun" {
		t.Errorf("targetContext = %q, want bun", res.TargetContext)
	}

	denoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(denoDir, "deno.jsonc"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = Scan(context.Background(), denoDir, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.TargetContext != "deno" {
		t.Errorf("targetContext = %q, want deno", res.TargetContext)
	}
	if res.Env.ExecutionContext == "deno" || res.Env.ExecutionContext == "bun" {
		t.Errorf("target context leaked into env: %q", res.Env.ExecutionContext)
	}
}

func TestPublicnessCacheRoundTrip(t *testing.T) {
	db := testDB(t)
	cache := PublicnessCache{DB: db}
	ctx := context.Background()

	if _, _, ok := cache.GetPublicness(ctx, "pkg:npm/axios@1.12.0"); ok {
		t.Fatal("empty cache reported a verdict")
	}
	if err := cache.SetPublicness(ctx, "pkg:npm/axios@1.12.0", scanner.PublicnessPublic); err != nil {
		t.Fatalf("SetPublicness: %v", err)
	}
	status, checkedAt, ok := cache.GetPublicness(ctx, "pkg:npm/axios@1.12.0")
	if !ok || status != scanner.PublicnessPublic {
		t.Fatalf("GetPublicness = (%q, %v), want PUBLIC", status, ok)
	}
	if checkedAt.IsZero() {
		t.Fatal("checkedAt not stamped")
	}
}
