package admin

import (
	"strings"
	"testing"
)

// The panel and the payload have to agree on where the matrix lives and on
// what the cells are called. They are written in two files and two languages,
// and the last time they disagreed the withdrawn panel rendered its empty
// state for weeks -- a `|| []` swallowed the undefined and nothing said so.
func TestCompletenessPanelReadsTheKeysTheServerSends(t *testing.T) {
	js, err := adminStaticFS.ReadFile("static/admin.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, key := range []string{
		"data.completeness", "dependencyGraph", "dependencyProvenNone", "dependencyUnknown",
	} {
		if !strings.Contains(src, key) {
			t.Errorf("admin.js never reads %s; farm_http.go sends it", key)
		}
	}
	// All eight cells, by name. A panel that renders only the non-zero ones
	// hides the two that are structurally empty, which is the finding.
	for _, cell := range []string{"SED", "SE-", "S-D", "S--", "-ED", "-E-", "--D", "---"} {
		if !strings.Contains(src, `"`+cell+`"`) {
			t.Errorf("admin.js does not render the %s cell", cell)
		}
	}
	// The container the template declares.
	html, err := templateFS.ReadFile("templates/admin.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `id="farm-completeness"`) {
		t.Error(`the farm tab has no #farm-completeness container for the script to fill`)
	}
}

// "No dependencies" and "nobody has looked" are the two readings this axis
// exists to keep apart, so the panel has to say which one it is showing.
// Rendering them under one label would be the network asserting a measurement
// it never made.
func TestCompletenessPanelNamesUnknownAndProvenNoneApart(t *testing.T) {
	js, err := adminStaticFS.ReadFile("static/admin.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, "의존성 미상") {
		t.Error("the panel does not label the unknown dependency state")
	}
	if !strings.Contains(src, "의존성 없음 (증명됨)") {
		t.Error("the panel does not label proven-no-dependencies as a measured absence")
	}
}
