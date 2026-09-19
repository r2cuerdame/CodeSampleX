package serverstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMonetizationProposalKeepsTheHabitAndOneProductGuardrails makes the
// decision in issue #205 durable. The proposal may be recalibrated before a
// launch, but it must not quietly turn local recall, contribution, or transport
// choices into separate paid products.
func TestMonetizationProposalKeepsTheHabitAndOneProductGuardrails(t *testing.T) {
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

	raw, err := os.ReadFile(filepath.Join(root, "docs", "monetization.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)

	for _, required := range []string{
		"unmetered local recall",
		"Evidence contribution",
		"Free / individual",
		"Pro / heavy individual",
		"Team / org",
		"Enterprise",
		"pooled hosted capacity",
		"Observed **2026-09-19**",
		"https://context7.com/plans",
		"Prices and numeric limits must pass the dated benchmark",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("docs/monetization.md no longer states guardrail %q", required)
		}
	}

	for _, fragmentedProduct := range []string{
		"MCP plan",
		"API plan",
		"pay per adapter",
	} {
		if strings.Contains(doc, fragmentedProduct) {
			t.Errorf("docs/monetization.md fragments the upgrade pack with %q", fragmentedProduct)
		}
	}
}
