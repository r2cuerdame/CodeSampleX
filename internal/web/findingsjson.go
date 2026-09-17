package web

import (
	"encoding/json"
	"net/http"
)

// GET /findings.json (#318).
//
// The findings page was the one public surface with no JSON form. The read
// API served packages, samples, wanted and stats; the contradictions -- the
// thing a model most often gets wrong, and the thing the project stands
// behind -- were HTML only, so a zero-install caller had to parse a page.
// This is the same collection the page renders, through the same filters,
// as a document. It lives here rather than in httpapi because the
// collection does: the hand-checked groups are Go literals in this package
// and the derived group is this package's cache of the store.

// findingsDocument is the whole collection after the filter, never a page
// of it. The HTML pages the derived group at findingsPerPage because a
// person reads one screen at a time; a caller that asked for JSON asked for
// the data, and a page number on a document is a second thing to get wrong.
type findingsDocument struct {
	SchemaVersion int `json:"schemaVersion"`
	// Filter echoes what was applied, cleaned to the vocabulary the page
	// accepts, so a caller can see that ?eco=NPM became "npm" and that a
	// value outside the vocabulary became nothing rather than an error.
	Filter   findingsDocumentFilter `json:"filter"`
	Total    int                    `json:"total"`
	Findings []findingEntry         `json:"findings"`
	// Note restates what a finding is, for a reader that has the document
	// and not the page around it.
	Note string `json:"note"`
}

type findingsDocumentFilter struct {
	Query     string `json:"q,omitempty"`
	Ecosystem string `json:"eco,omitempty"`
	OS        string `json:"os,omitempty"`
	Runtime   string `json:"runtime,omitempty"`
	Basis     string `json:"basis,omitempty"`
}

// findingEntry is one finding as the page shows it. Basis says how it
// entered the list: "docs" contradicts an official document, "belief" a
// common belief, "sample" is stated by a published sample's author and
// measured by its contract. Every entry with a sample links to the page
// whose receipts prove it.
type findingEntry struct {
	Ecosystem   string `json:"ecosystem"`
	Subject     string `json:"subject"`
	Believed    string `json:"believed"`
	Measured    string `json:"measured"`
	Basis       string `json:"basis"`
	SampleID    string `json:"sampleId,omitempty"`
	SampleURL   string `json:"sampleUrl,omitempty"`
	SourceURL   string `json:"sourceUrl,omitempty"`
	SourceLabel string `json:"sourceLabel,omitempty"`
	OS          string `json:"os,omitempty"`
	Runtime     string `json:"runtime,omitempty"`
	Environment string `json:"environment,omitempty"`
}

const findingsDocumentNote = "A finding is a measured contradiction: what was believed, next to what a " +
	"verified sample's contract measured. Each one links to the published sample whose " +
	"receipts prove it; re-run the sample to disagree. os and runtime are what the sample " +
	"recorded, never inferred from the ecosystem; an empty dimension is unknown."

func (s *site) findingsJSON(w http.ResponseWriter, r *http.Request) {
	filter := cleanFindingsFilter(findingsFilter{
		Query:     r.URL.Query().Get("q"),
		Ecosystem: r.URL.Query().Get("eco"),
		OS:        r.URL.Query().Get("os"),
		Runtime:   r.URL.Query().Get("runtime"),
		Basis:     r.URL.Query().Get("basis"),
	})
	documented, believed := s.handFindings(r)
	groups := [][]finding{
		filterFindings(documented, filter),
		filterFindings(believed, filter),
		filterFindings(s.derivedFindings(r), filter),
	}
	base := s.base(r)
	doc := findingsDocument{
		SchemaVersion: 1,
		Filter: findingsDocumentFilter{
			Query: filter.Query, Ecosystem: filter.Ecosystem, OS: filter.OS,
			Runtime: filter.Runtime, Basis: filter.Basis,
		},
		Findings: []findingEntry{},
		Note:     findingsDocumentNote,
	}
	for _, group := range groups {
		for _, f := range group {
			entry := findingEntry{
				Ecosystem: f.Ecosystem, Subject: f.Subject, Believed: f.Believed,
				Measured: f.Measured, Basis: f.Basis, SampleID: f.SampleID,
				SourceURL: f.SourceURL, SourceLabel: f.SourceLabel,
				OS: f.OS, Runtime: f.Runtime, Environment: f.Environment,
			}
			if f.SampleID != "" {
				entry.SampleURL = base + sampleHref(f.SampleID)
			}
			doc.Findings = append(doc.Findings, entry)
		}
	}
	doc.Total = len(doc.Findings)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
}
