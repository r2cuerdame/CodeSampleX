package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This is the fixture that keeps the SERP copy honest against the corpus
// that actually exists, rather than against the one a test author would
// have invented.
//
// testdata/production-samples-2026-08-27.json holds 24 published samples,
// read from the public API on the day the Search Console export was taken,
// across eight ecosystems. Nine of the 24 carry the goal the authoring
// worker prints — which is what made the live titles unsearchable, and
// which no hand-written fixture would have contained.
//
// Run it with -v to print the whole before/after table: that is the
// evidence for "a person can tell what this sample is from the search
// result", and it is the table R2C-205 asks for over at least twenty
// representative samples.

type corpusSample struct {
	SampleID       string   `json:"sampleId"`
	Status         string   `json:"status"`
	Goal           string   `json:"goal"`
	Packages       []string `json:"packages"`
	Symbols        []string `json:"symbols"`
	Contract       []string `json:"contract"`
	RunEnvironment string   `json:"runEnvironment"`
	ContractPassed bool     `json:"contractPassed"`
}

type corpusDoc struct {
	Source  string         `json:"source"`
	Samples []corpusSample `json:"samples"`
}

func loadCorpus(t *testing.T) corpusDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "production-samples-2026-08-27.json"))
	if err != nil {
		t.Fatalf("read production corpus: %v", err)
	}
	var doc corpusDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse production corpus: %v", err)
	}
	if len(doc.Samples) < 20 {
		t.Fatalf("corpus holds %d samples; the contract is at least 20", len(doc.Samples))
	}
	return doc
}

// oldTitle is the title the site produced before this change: the goal,
// then the package label, then the brand. It is reproduced here so the
// table below shows what was actually replaced rather than a description
// of it.
func oldTitle(s corpusSample) string {
	goal := strings.TrimSpace(s.Goal)
	if goal == "" {
		return "Sample " + shortHash(s.SampleID) + " — CodeSampleX"
	}
	title := goal
	if refs := packageRefs(s.Packages); len(refs) > 0 {
		title += " · " + refs[0].Label
	}
	return title + " — CodeSampleX"
}

func corpusCopy(s corpusSample) serpCopy {
	eco, name, version := sampleRelease(s.Packages)
	return buildSerpCopy("en", serpInput{
		SampleID: s.SampleID, Ecosystem: eco, Name: name, Version: version,
		Goal: s.Goal, Symbols: s.Symbols, Contract: s.Contract,
		RunEnvironment: s.RunEnvironment, Verified: s.ContractPassed,
	})
}

// Every one of these has to hold on every sample in the corpus. They are
// the properties that decide whether a search result is worth clicking.
func TestProductionCorpusProducesSearchableTitles(t *testing.T) {
	doc := loadCorpus(t)
	for _, s := range doc.Samples {
		eco, name, version := sampleRelease(s.Packages)
		if name == "" || version == "" {
			t.Errorf("%s: no release could be read from %v", s.SampleID, s.Packages)
			continue
		}
		copy := corpusCopy(s)

		// 1. The searched coordinate leads. Every zero-click query in the
		//    export was a package or a package and a release.
		if !strings.HasPrefix(copy.Title, name+" "+version) {
			t.Errorf("%s: title does not lead with the release: %q", s.SampleID, copy.Title)
		}
		// 2. No internal identifier reaches the title. The purl, its percent
		//    escapes and the content address are all identity, not language.
		for _, banned := range []string{"pkg:", "%40", "sha256:"} {
			if strings.Contains(copy.Title, banned) {
				t.Errorf("%s: title carries %q: %q", s.SampleID, banned, copy.Title)
			}
		}
		// 3. "Verified" is said when and only when a receipt says a contract
		//    passed. This is the honesty rule, checked on real statuses.
		verified := strings.Contains(copy.Title, "Verified sample")
		if verified != s.ContractPassed {
			t.Errorf("%s: title says verified=%v, receipts say %v: %q",
				s.SampleID, verified, s.ContractPassed, copy.Title)
		}
		// 4. The description fits a snippet and still names the release.
		if n := len([]rune(copy.Description)); n > descriptionBudget {
			t.Errorf("%s: description is %d characters: %q", s.SampleID, n, copy.Description)
		}
		if !strings.Contains(copy.Description, version) {
			t.Errorf("%s: description does not name the release: %q", s.SampleID, copy.Description)
		}
		// 5. Every one has a readable URL, and it round-trips through the
		//    router — these go into rel=canonical and into the sitemap.
		href := semanticSampleHref(eco, name, version, copy.Slug)
		if href == "" {
			t.Errorf("%s: no readable URL for %s/%s@%s", s.SampleID, eco, name, version)
			continue
		}
		if strings.Contains(strings.TrimPrefix(href, "/"+eco+"/"), "//") {
			t.Errorf("%s: readable URL has an empty segment: %q", s.SampleID, href)
		}
	}
}

// The subject is what a title says beyond the coordinate, and a title that
// is only a coordinate does not distinguish two samples about the same
// release. It cannot be demanded of every sample — some manifests name no
// API and carry a machine goal, and inventing a subject for those is worse
// than leaving the coordinate to stand alone — so it is measured, with a
// floor, rather than asserted one by one.
func TestProductionCorpusTitlesSayMoreThanTheCoordinate(t *testing.T) {
	doc := loadCorpus(t)
	withSubject := 0
	for _, s := range doc.Samples {
		if sampleSubject(nameOf(s), s.Goal, s.Symbols) != "" {
			withSubject++
		}
	}
	// Measured on this corpus at the time it was captured: 24 of 24.
	if min := len(doc.Samples) * 4 / 5; withSubject < min {
		t.Errorf("%d of %d titles say only the coordinate; at most a fifth may",
			len(doc.Samples)-withSubject, len(doc.Samples))
	}
	t.Logf("subject beyond the coordinate: %d/%d", withSubject, len(doc.Samples))
}

func nameOf(s corpusSample) string {
	_, name, _ := sampleRelease(s.Packages)
	return name
}

// The before/after table. It asserts nothing the two tests above do not
// already assert; it exists so a person can read what a searcher would see
// and say whether it answers the question they typed.
func TestProductionCorpusBeforeAndAfter(t *testing.T) {
	doc := loadCorpus(t)
	t.Logf("corpus source: %s", doc.Source)
	for i, s := range doc.Samples {
		copy := corpusCopy(s)
		eco, name, version := sampleRelease(s.Packages)
		t.Logf("\n[%02d] %s\n  before title: %s\n   after title: %s\n         url: %s\n        desc: %s",
			i+1, s.SampleID[:22]+"…",
			oldTitle(s), copy.Title,
			semanticSampleHref(eco, name, version, copy.Slug),
			copy.Description)
	}
}
