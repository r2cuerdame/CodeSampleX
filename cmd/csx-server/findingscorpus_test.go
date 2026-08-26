package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// findingManifest is a sample that states a belief and measures it in prose,
// which is what makes it a finding.
func findingManifest(subject string) string {
	return fmt.Sprintf(`{
		"schemaVersion":1,
		"packages":["pkg:npm/%s@1.0.0"],
		"case":{"schemaVersion":1,"kind":"HOW","goal":"call it once",
			"believed":"%s retries automatically",
			"packages":["pkg:npm/%s@1.0.0"],
			"contract":["the server receives one request and no automatic retry is added"]}}`,
		subject, subject, subject)
}

// plainManifest is a perfectly good verified sample that states no belief, so
// it is not a finding and never becomes one.
func plainManifest(subject string) string {
	return fmt.Sprintf(`{
		"schemaVersion":1,
		"packages":["pkg:npm/%s@1.0.0"],
		"case":{"schemaVersion":1,"kind":"HOW","goal":"call it once",
			"packages":["pkg:npm/%s@1.0.0"],
			"contract":["the call returns"]}}`, subject, subject)
}

// saveVerified stores a sample with an actual contract-PASS receipt.
func saveVerified(t *testing.T, store *serverstore.Fake, id, manifest string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID: id, ManifestJSON: manifest, Status: "PUBLISHED", CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
		ReceiptID: "receipt-" + id, SampleID: id,
		ContractResult: "PASS", ReceiptJSON: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
}

// A finding is a property of the corpus, not of its newest slice.
//
// DerivedFindings used to read the newest 2,000 verified samples and look for
// beliefs inside that window. Production grew past 2,000 verified samples and
// the public count fell from 543 to 250: every older finding aged out of a
// window it had no way to stay inside. Growth is not a takedown.
func TestDerivedFindingsSurviveCorpusGrowthPastAnyScanWindow(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	w := &webStore{s: store}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// The oldest samples are the ones that state beliefs.
	for i := 0; i < 5; i++ {
		saveVerified(t, store, fmt.Sprintf("sha256:old-finding-%02d", i),
			findingManifest(fmt.Sprintf("oldlib%02d", i)), base.Add(time.Duration(i)*time.Minute))
	}
	// Then the network publishes far more than one scan window of verified
	// samples that state none.
	for i := 0; i < 2100; i++ {
		saveVerified(t, store, fmt.Sprintf("sha256:new-plain-%05d", i),
			plainManifest(fmt.Sprintf("newlib%05d", i)), base.Add(time.Duration(1000+i)*time.Minute))
	}

	findings, err := w.DerivedFindings(ctx, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 5 {
		t.Fatalf("findings behind %d newer verified samples = %d, want 5", 2100, len(findings))
	}

	// Publishing more non-findings cannot lower the count.
	for i := 0; i < 500; i++ {
		saveVerified(t, store, fmt.Sprintf("sha256:more-plain-%05d", i),
			plainManifest(fmt.Sprintf("morelib%05d", i)), base.Add(time.Duration(9000+i)*time.Minute))
	}
	grown, err := w.DerivedFindings(ctx, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(grown) != 5 {
		t.Fatalf("findings after 500 more non-finding samples = %d, want 5", len(grown))
	}

	// A new finding raises it.
	saveVerified(t, store, "sha256:new-finding", findingManifest("freshlib"),
		base.Add(20000*time.Minute))
	added, err := w.DerivedFindings(ctx, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 6 {
		t.Fatalf("findings after publishing one finding = %d, want 6", len(added))
	}

	// A source-only upload — no contract-PASS receipt — is not a finding.
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID: "sha256:source-only", ManifestJSON: findingManifest("sourceonly"),
		Status: "PUBLISHED", CreatedAt: base.Add(20001 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	unproved, err := w.DerivedFindings(ctx, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(unproved) != 6 {
		t.Fatalf("findings after a source-only upload = %d, want 6", len(unproved))
	}

	// Quarantine is the only thing that removes one.
	if err := store.SetSampleQuarantine(ctx, "sha256:old-finding-00", true, "takedown"); err != nil {
		t.Fatal(err)
	}
	after, err := w.DerivedFindings(ctx, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 5 {
		t.Fatalf("findings after quarantining one = %d, want 5", len(after))
	}
	for _, f := range after {
		if f.SampleID == "sha256:old-finding-00" {
			t.Fatalf("quarantined sample still published as a finding")
		}
	}
}

// Paging must not lose a finding at a page boundary, and must not return one
// twice. A keyset cursor is the reason it cannot: an offset would do both the
// moment a sample is published mid-scan.
func TestDerivedFindingsPageThroughEveryEligibleSampleExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	w := &webStore{s: store}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const total = 2*derivedFindingPage + 137 // spans three reads, ends short
	for i := 0; i < total; i++ {
		saveVerified(t, store, fmt.Sprintf("sha256:finding-%05d", i),
			findingManifest(fmt.Sprintf("lib%05d", i)), base.Add(time.Duration(i)*time.Minute))
	}

	findings, err := w.DerivedFindings(ctx, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != total {
		t.Fatalf("findings = %d, want %d", len(findings), total)
	}
	seen := make(map[string]bool, len(findings))
	for _, f := range findings {
		if seen[f.SampleID] {
			t.Fatalf("sample %s returned twice", f.SampleID)
		}
		seen[f.SampleID] = true
	}
	if len(seen) != total {
		t.Fatalf("distinct samples = %d, want %d", len(seen), total)
	}
	// Newest first, across page boundaries as well as inside them.
	if findings[0].SampleID != fmt.Sprintf("sha256:finding-%05d", total-1) {
		t.Fatalf("first finding = %s, want the newest", findings[0].SampleID)
	}
	if findings[len(findings)-1].SampleID != "sha256:finding-00000" {
		t.Fatalf("last finding = %s, want the oldest", findings[len(findings)-1].SampleID)
	}

	// limit still bounds the answer, and takes the newest.
	capped, err := w.DerivedFindings(ctx, 600)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != 600 || capped[0].SampleID != findings[0].SampleID {
		t.Fatalf("capped findings = %d starting at %s", len(capped), capped[0].SampleID)
	}
}
