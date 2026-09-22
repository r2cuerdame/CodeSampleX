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
func findRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("no go.mod above the test working directory")
		}
		root = parent
	}
}

// TestDataRightsMapDoesNotOverstateTheCurrentSystem keeps the three review
// corrections attached to the document that readers will use for the legal
// decision. These are factual storage/deployment boundaries, not licence
// choices: changing one requires changing the implementation or this map.
func TestDataRightsMapDoesNotOverstateTheCurrentSystem(t *testing.T) {
	root := findRepoRoot(t)

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

// TestDataRightsThreeLayersDocumentedAcrossREADMEs pins the three-layer
// licensing statement (Code: Apache-2.0, Samples: MIT-0, Data: CDLA-Permissive-2.0)
// and badge disambiguation across README and its eight translations, ensuring
// no edit collapses the rights model back to two layers.
func TestDataRightsThreeLayersDocumentedAcrossREADMEs(t *testing.T) {
	root := findRepoRoot(t)

	// DATA_TERMS.md must exist at root, be non-empty, and name the instrument.
	dataTermsRaw, err := os.ReadFile(filepath.Join(root, "DATA_TERMS.md"))
	if err != nil {
		t.Fatalf("DATA_TERMS.md missing at root: %v", err)
	}
	dataTerms := string(dataTermsRaw)
	for _, term := range []string{
		"CDLA-Permissive-2.0",
		"Community Data License Agreement",
		"September 21, 2026",
		"Contributor Grant",
		"Four Rights Layers",
	} {
		if !strings.Contains(dataTerms, term) {
			t.Errorf("DATA_TERMS.md missing key section/term %q", term)
		}
	}

	readmes := []string{
		"README.md",
		filepath.Join("docs", "i18n", "README.ko.md"),
		filepath.Join("docs", "i18n", "README.ja.md"),
		filepath.Join("docs", "i18n", "README.zh-CN.md"),
		filepath.Join("docs", "i18n", "README.es.md"),
		filepath.Join("docs", "i18n", "README.fr.md"),
		filepath.Join("docs", "i18n", "README.de.md"),
		filepath.Join("docs", "i18n", "README.pt-BR.md"),
		filepath.Join("docs", "i18n", "README.ru.md"),
	}

	for _, rel := range readmes {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		s := string(b)

		// Must name all three layers and link DATA_TERMS.md
		if !strings.Contains(s, "Apache-2.0") {
			t.Errorf("%s missing code license (Apache-2.0)", rel)
		}
		if !strings.Contains(s, "MIT-0") {
			t.Errorf("%s missing sample license default (MIT-0)", rel)
		}
		if !strings.Contains(s, "CDLA-Permissive-2.0") {
			t.Errorf("%s missing data license (CDLA-Permissive-2.0)", rel)
		}
		if !strings.Contains(s, "DATA_TERMS.md") {
			t.Errorf("%s does not link DATA_TERMS.md", rel)
		}

		// Must disambiguate badge: code license badge must specify label=code
		if !strings.Contains(s, "label=code") {
			t.Errorf("%s code license badge not disambiguated with ?label=code", rel)
		}
	}

	// PRIVACY.md must cross-reference DATA_TERMS.md under §7
	privacyRaw, err := os.ReadFile(filepath.Join(root, "PRIVACY.md"))
	if err != nil {
		t.Fatalf("PRIVACY.md missing: %v", err)
	}
	if !strings.Contains(string(privacyRaw), "DATA_TERMS.md") {
		t.Error("PRIVACY.md does not reference DATA_TERMS.md")
	}
}
