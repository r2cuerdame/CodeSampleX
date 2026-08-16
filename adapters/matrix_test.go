package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The published capability matrix is a static file, schemas/v1/adapters.json,
// and the website renders it verbatim as a public claim about what this
// project can observe in somebody's repository.
//
// Every adapter already declares the same thing in code, through
// Capabilities(). Two declarations of one fact drift, and this one drifts
// in the direction that matters: the file keeps claiming a level after the
// code stops providing it, and nothing anywhere notices, because the page
// reads the file and the scanner reads the code.
//
// So the file may not say more than the implementations do.
func TestPublishedMatrixMatchesTheAdapters(t *testing.T) {
	doc := loadMatrix(t)
	inCode := map[string][]string{}
	for _, a := range All() {
		inCode[a.Ecosystem()] = a.Capabilities()
	}

	for _, entry := range doc.Adapters {
		claimed := append([]string(nil), entry.Capabilities...)
		sort.Strings(claimed)

		actual, has := inCode[entry.Ecosystem]
		if !has {
			// No adapter at all. A4 is the sandbox verifying a published
			// sample, which does not need one — every other level is a
			// statement about reading a user's project, and there is
			// nothing here that can read it.
			for _, c := range claimed {
				if c != "A4" {
					t.Errorf("%s claims %s with no adapter registered: nothing "+
						"can observe a %s project at all", entry.Ecosystem, c, entry.Ecosystem)
				}
			}
			continue
		}
		sorted := append([]string(nil), actual...)
		sort.Strings(sorted)
		// A4 comes from the sandbox rather than the adapter, so the file
		// may carry it either way.
		if strings.Join(withoutA4(claimed), ",") != strings.Join(withoutA4(sorted), ",") {
			t.Errorf("%s: the file publishes %v, the adapter reports %v",
				entry.Ecosystem, claimed, actual)
		}
	}

	// And every adapter that exists must appear, or the page silently
	// under-claims a capability somebody could be using.
	for eco := range inCode {
		found := false
		for _, entry := range doc.Adapters {
			if entry.Ecosystem == eco {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has an adapter but no row in the published matrix", eco)
		}
	}
}

// Symbol confidence is a claim about accuracy, and only an adapter that
// actually extracts symbols may make one above UNKNOWN.
func TestSymbolConfidenceNeedsAnAdapter(t *testing.T) {
	doc := loadMatrix(t)
	has := map[string]bool{}
	for _, a := range All() {
		has[a.Ecosystem()] = true
	}
	for _, e := range doc.Adapters {
		if e.SymbolConfidence != "" && e.SymbolConfidence != "UNKNOWN" && !has[e.Ecosystem] {
			t.Errorf("%s publishes symbolConfidence %q with no adapter to extract symbols",
				e.Ecosystem, e.SymbolConfidence)
		}
	}
}

type matrixDoc struct {
	Adapters []struct {
		Ecosystem        string   `json:"ecosystem"`
		Capabilities     []string `json:"capabilities"`
		SymbolConfidence string   `json:"symbolConfidence"`
	} `json:"adapters"`
}

func loadMatrix(t *testing.T) matrixDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "schemas", "v1", "adapters.json"))
	if err != nil {
		t.Fatalf("the published matrix is unreadable: %v", err)
	}
	var doc matrixDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Adapters) == 0 {
		t.Fatal("the published matrix is empty")
	}
	return doc
}

func withoutA4(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "A4" {
			out = append(out, s)
		}
	}
	return out
}
