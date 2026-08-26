package serverstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDataRightsMapDoesNotOverstateTheCurrentSystem keeps the three review
// corrections attached to the document that readers will use for the legal
// decision. These are factual storage/deployment boundaries, not licence
// choices: changing one requires changing the implementation or this map.
func TestDataRightsMapDoesNotOverstateTheCurrentSystem(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("no go.mod above the test working directory")
		}
		root = parent
	}

	raw, err := os.ReadFile(filepath.Join(root, "docs", "data-rights.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)

	for _, required := range []string{
		"client-supplied observation day",
		"no non-test caller",
		"no reliable server-side contribution cutoff",
		"optional GitHub identity records",
		"IP-derived, epoch-scoped activity pseudonyms",
		"authoring-session metadata including refresh IP and computer name",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("docs/data-rights.md no longer states factual boundary %q", required)
		}
	}

	for _, falseClaim := range []string{
		"A date-bounded grant is only expressible for 30 days",
		"only within the 30-day retention window",
		"Personal data** | none is collected",
	} {
		if strings.Contains(doc, falseClaim) {
			t.Errorf("docs/data-rights.md restored disproved claim %q", falseClaim)
		}
	}
}
