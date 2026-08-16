package samples

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// believedManifest is a minimal publishable manifest with one belief.
func believedManifest(believed string) domain.SampleManifest {
	return domain.SampleManifest{
		Packages: []string{"pkg:npm/axios@1.12.0"},
		Case: domain.Case{
			Kind:     "HOW",
			Goal:     "Send a POST with a JSON body",
			Packages: []string{"pkg:npm/axios@1.12.0"},
			Contract: []string{"the server receives application/json"},
			Believed: believed,
		},
	}
}

func createWithBelieved(t *testing.T, believed string) error {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.js"), []byte("// sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := CreateFromDir(context.Background(), dir, believedManifest(believed))
	return err
}

func TestBelievedIsBounded(t *testing.T) {
	if err := createWithBelieved(t, "axios sets the header itself"); err != nil {
		t.Fatalf("a one-sentence belief must be accepted: %v", err)
	}
	err := createWithBelieved(t, strings.Repeat("a", maxBelievedBytes+1))
	if err == nil {
		t.Fatal("an over-long belief must be refused, not truncated: it is published as prose")
	}
	if !strings.Contains(err.Error(), "believed") {
		t.Errorf("the refusal must name the field the author has to fix, got %v", err)
	}
}

// A belief that repeats the goal contradicts nothing, and a findings page
// full of those is worse than a shorter one.
func TestBelievedMayNotRepeatTheGoal(t *testing.T) {
	if err := createWithBelieved(t, "  send a POST with a JSON body  "); err == nil {
		t.Fatal("a belief that is the goal in different whitespace must be refused")
	}
}

// The field is optional and omitted when empty, which is what keeps every
// case id computed before it existed unchanged.
func TestBelievedDoesNotChangeExistingCaseIDs(t *testing.T) {
	c := domain.Case{
		SchemaVersion: 1,
		Kind:          "HOW",
		Goal:          "Send a POST with a JSON body",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Contract:      []string{"the server receives application/json"},
	}
	before := c.ComputeID()
	if got := string(domain.MustCanonicalJSON(c)); strings.Contains(got, "believed") {
		t.Fatalf("an unset belief must not appear in the canonical JSON: %s", got)
	}
	c.Believed = "axios sets the header itself"
	if c.ComputeID() == before {
		t.Fatal("a case that now states a belief is a different case and must get a different id")
	}
	c.Believed = ""
	if c.ComputeID() != before {
		t.Fatal("clearing the belief must restore the original id")
	}
}
