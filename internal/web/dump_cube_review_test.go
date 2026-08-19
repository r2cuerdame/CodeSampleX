package web

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestDumpCubeReview writes the matrix-first pages rendered from the cube
// fixture to CSX_CUBE_DUMP for manual design review. Skipped unless set.
func TestDumpCubeReview(t *testing.T) {
	dir := os.Getenv("CSX_CUBE_DUMP")
	if dir == "" {
		t.Skip("CSX_CUBE_DUMP not set")
	}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = matrixStore() })
	pages := map[string]string{
		"landing.html":    "/",
		"landing-ko.html": "/ko/",
		"package.html":    "/npm/reactish",
		"drill.html":      "/npm/reactish?f_os=linux",
		"leaf.html":       "/npm/reactish?f_os=windows&f_runtime=node+22",
		"version.html":    "/npm/reactish/19.1.0",
		"symbol.html":     "/npm/reactish/19.1.0/createRoot",
	}
	for name, path := range pages {
		rec := get(t, mux, path)
		body := bytes.ReplaceAll(rec.Body.Bytes(), []byte(`"/static/`), []byte(`"static/`))
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.MkdirAll(filepath.Join(dir, "static"), 0o755)
	for _, name := range []string{"site.css", "inspector-hero-v1.webp"} {
		asset, err := os.ReadFile(filepath.Join("static", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "static", name), asset, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
