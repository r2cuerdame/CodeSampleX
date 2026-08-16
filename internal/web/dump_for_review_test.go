package web

import (
	"bytes"
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
		"landing-de.html": "/de/",
		"landing-ru.html": "/ru/",
		"symbol.html":     "/npm/axios/1.12.0/axios.post",
		"package.html":    "/npm/axios",
		"sample.html":     "/samples/sha256:d1e2f3",
		"explore.html":    "/explore",
		"adapters.html":   "/adapters",
		"stats.html":      "/stats",
	}
	for name, path := range pages {
		rec := get(t, mux, path)
		body := bytes.ReplaceAll(rec.Body.Bytes(), []byte(`"/static/`), []byte(`"static/`))
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Copy the self-hosted landing assets next to the dumps so file:// review
	// works without a server or a network connection.
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
