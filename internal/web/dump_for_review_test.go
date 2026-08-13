package web

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDumpForReview writes rendered pages to CSX_WEB_DUMP for manual
// design review. Skipped unless the env var is set.
func TestDumpForReview(t *testing.T) {
	dir := os.Getenv("CSX_WEB_DUMP")
	if dir == "" {
		t.Skip("CSX_WEB_DUMP not set")
	}
	mux, _ := newTestMux(t, nil)
	pages := map[string]string{
		"landing.html":    "/",
		"landing-ko.html": "/ko/",
		"symbol.html":     "/npm/axios/1.12.0/axios.post",
		"package.html":    "/npm/axios",
		"sample.html":     "/samples/sha256:d1e2f3",
		"explore.html":    "/explore",
		"adapters.html":   "/adapters",
		"stats.html":      "/stats",
	}
	for name, path := range pages {
		rec := get(t, mux, path)
		if err := os.WriteFile(filepath.Join(dir, name), rec.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Copy the stylesheet next to the dumps so file:// review works.
	css, err := os.ReadFile("static/site.css")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "static"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "static", "site.css"), css, 0o644); err != nil {
		t.Fatal(err)
	}
}
