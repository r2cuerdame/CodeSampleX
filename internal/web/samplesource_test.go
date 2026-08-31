package web

import (
	"strings"
	"testing"
)

// A sample page shows the source, not just its filenames.
//
// Naming the files and offering a tarball let a visitor see that a sample
// exists without seeing what it says. Downloading an archive to read forty
// lines of Go is not inspection — it is a barrier with a link on it, and the
// source is the thing the page is about.
func TestASamplePageShowsItsSource(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.sampleSource = map[string][]SampleFile{
		"sha256:d1e2f3": {
			{Name: "csx.json", Body: `{"schemaVersion":1}`},
			{Name: "main.go", Body: "package main\n\nfunc main() {}\n"},
		},
	}
	body := get(t, mux, "/samples/sha256:d1e2f3").Body.String()

	for _, want := range []string{"csx.json", "main.go", "func main()", "schemaVersion"} {
		if !strings.Contains(body, want) {
			t.Errorf("%q missing from the sample page", want)
		}
	}
	if !strings.Contains(body, "<pre>") {
		t.Error("the source is not rendered as preformatted text")
	}
	// The archive stays. Reading on the page and taking the whole sample away
	// are different things a reader wants at different moments.
	if !strings.Contains(body, "/v1/samples/sha256:d1e2f3/artifact") {
		t.Error("the download link was dropped when the source arrived")
	}
}

// Source is contributed by other people and rendered on a public page, so it
// is escaped as text and never as markup.
//
// This is the one property that has to hold no matter what a sample contains.
// html/template escapes by default, which is exactly why it is worth a test:
// nothing in the template says "escape this", so nothing would say it if a
// later change reached for template.HTML to get syntax colouring.
func TestSampleSourceIsRenderedAsTextNotMarkup(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.sampleSource = map[string][]SampleFile{
		"sha256:d1e2f3": {{
			Name: "evil.html",
			Body: `<script>alert(1)</script><img src=x onerror=alert(2)>`,
		}},
	}
	body := get(t, mux, "/samples/sha256:d1e2f3").Body.String()

	// The property is that no TAG from the source reaches the document. The
	// attribute text does survive -- inside an escaped entity sequence, where
	// it is inert -- and asserting on that substring would be measuring the
	// wrong thing: it fails on correct output and would push a later reader
	// towards stripping text that is safe.
	//
	// Rendered, the block reads:
	//   <pre><code>&lt;script&gt;alert(1)&lt;/script&gt;&lt;img src=x onerror=alert(2)&gt;</code></pre>
	for _, raw := range []string{"<script>alert(1)", "<img src=x"} {
		if strings.Contains(body, raw) {
			t.Errorf("a sample's source produced live markup: %q", raw)
		}
	}
	// It is still readable as text.
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the source was not rendered at all")
	}
	// And a filename gets the same treatment.
	store.sampleSource["sha256:d1e2f3"][0].Name = `<b>bold</b>.go`
	body = get(t, mux, "/samples/sha256:d1e2f3").Body.String()
	if strings.Contains(body, "<b>bold</b>.go") {
		t.Error("a filename was rendered as markup")
	}
}

// A cut file says so. A reader must not copy half a file believing it whole.
func TestATruncatedSourceFileSaysSo(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.sampleSource = map[string][]SampleFile{
		"sha256:d1e2f3": {{Name: "big.txt", Body: "xxxx", Truncated: true}},
	}
	body := get(t, mux, "/samples/sha256:d1e2f3").Body.String()
	if !strings.Contains(body, "first part only") {
		t.Error("a truncated file was shown without saying it was cut")
	}
}

// A sample whose artifact cannot be read still has a page.
//
// Source is an improvement on naming the files and offering the archive, never
// a precondition for it: a storage hiccup must not turn a sample into a 404 or
// an error page.
func TestASamplePageSurvivesAnUnreadableArtifact(t *testing.T) {
	mux, _ := newTestMux(t, nil) // no sampleSource seeded at all
	rec := get(t, mux, "/samples/sha256:d1e2f3")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/v1/samples/sha256:d1e2f3/artifact") {
		t.Error("the archive is not offered when the source is unavailable")
	}
	if strings.Contains(body, `id="sample-source-heading"`) {
		t.Error("an empty source section was rendered")
	}
}
