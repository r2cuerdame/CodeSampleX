package web

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// GET /findings.json (#318). The findings page was the one public surface
// with no JSON form: the read API served packages, samples, stats and
// wanted, and a zero-install caller who wanted the contradictions had to
// parse HTML. This is the same collection the page renders, same filters,
// as a document.
func TestFindingsJSONIsTheSameCollectionThePageRenders(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.derived = []DerivedFinding{{
		Ecosystem: "pypi",
		Subject:   "httpx@0.28.1",
		Believed:  "a timeout of 5 covers the whole request",
		Measured:  "connect, read, write and pool each get their own 5 seconds",
		SampleID:  "sha256:aaaa000000000000000000000000000000000000000000000000000000000001",
		OS:        "linux",
		Runtime:   "python",
	}}

	var doc findingsDocument
	deadline := time.Now().Add(time.Second)
	for {
		rec := get(t, mux, "/findings.json")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Fatalf("Content-Type = %q", ct)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, rec.Body.String())
		}
		if containsSubject(doc.Findings, "httpx@0.28.1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the derived finding never appeared: %s", rec.Body.String())
		}
		time.Sleep(time.Millisecond)
	}
	if doc.SchemaVersion != 1 {
		t.Errorf("schemaVersion = %d", doc.SchemaVersion)
	}
	if doc.Total != len(doc.Findings) {
		t.Errorf("total %d != %d findings listed; the document is unfiltered so they must agree", doc.Total, len(doc.Findings))
	}
	// The hand-checked groups are in the same document, with their basis.
	seen := map[string]bool{}
	for _, entry := range doc.Findings {
		seen[entry.Basis] = true
		if entry.SampleID != "" && entry.SampleURL != "https://codesamplex.dev/samples/"+entry.SampleID {
			t.Errorf("%s: sampleUrl %q is not the canonical page", entry.Subject, entry.SampleURL)
		}
		if entry.Subject == "" || entry.Believed == "" || entry.Measured == "" {
			t.Errorf("an entry is missing its subject, belief or measurement: %+v", entry)
		}
	}
	for _, basis := range []string{"docs", "belief", "sample"} {
		if !seen[basis] {
			t.Errorf("no finding with basis %q in the document", basis)
		}
	}

	// The page's filters are the document's filters.
	rec := get(t, mux, "/findings.json?eco=pypi&os=linux&runtime=python&q=timeout")
	var narrowed findingsDocument
	if err := json.Unmarshal(rec.Body.Bytes(), &narrowed); err != nil {
		t.Fatal(err)
	}
	if !containsSubject(narrowed.Findings, "httpx@0.28.1") {
		t.Errorf("the filtered document lost the row that matches every filter: %s", rec.Body.String())
	}
	for _, entry := range narrowed.Findings {
		if entry.Ecosystem != "pypi" {
			t.Errorf("eco=pypi returned a %s finding", entry.Ecosystem)
		}
	}
	if narrowed.Total != len(narrowed.Findings) || narrowed.Total >= doc.Total {
		t.Errorf("filtered total %d of %d: the filter did not narrow", narrowed.Total, doc.Total)
	}
	if narrowed.Filter.Ecosystem != "pypi" || narrowed.Filter.Query != "timeout" {
		t.Errorf("the document does not echo the filter it applied: %+v", narrowed.Filter)
	}
}

func containsSubject(list []findingEntry, subject string) bool {
	for _, entry := range list {
		if entry.Subject == subject {
			return true
		}
	}
	return false
}
