package seoreport

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baselinePath is the pre-deploy measurement this change is judged against.
const baselinePath = "../../docs/seo/serp-baseline-2026-08-27.json"

// The baseline exists so the change can be judged later, which only works if
// it is still there and still says what it said. These are the figures the
// 2026-08-27 export produced, before anything in this branch was deployed.
func TestStoredBaselineHoldsTheMeasurementItWasStoredFor(t *testing.T) {
	f, err := os.Open(filepath.FromSlash(baselinePath))
	if err != nil {
		t.Fatalf("the pre-deploy baseline is missing: %v", err)
	}
	defer f.Close()
	snap, err := ReadJSON(f)
	if err != nil {
		t.Fatal(err)
	}

	sample := snap.Cohorts[ClassSample].All
	if sample.Pages != 187 || sample.Impressions != 1546 || sample.Clicks != 0 {
		t.Errorf("sample cohort = %+v, want 187 pages / 1546 impressions / 0 clicks", sample)
	}
	if snap.SampleTop10.Pages != 157 || snap.SampleTop10.Impressions != 1393 ||
		snap.SampleTop10.Clicks != 0 {
		t.Errorf("page-one sample population = %+v, want 157 pages / 1393 impressions / 0 clicks",
			snap.SampleTop10)
	}
	// The zero-click queries are the evidence that this was a snippet
	// problem and not a ranking one: every one of them ranks on page one.
	if len(snap.Queries) < 4 {
		t.Fatalf("baseline kept %d query rows, want the four that were recorded", len(snap.Queries))
	}
	for _, q := range snap.Queries {
		if q.Clicks != 0 {
			t.Errorf("query %q recorded %d clicks; the baseline is zero-click by definition",
				q.Key, q.Clicks)
		}
		if q.Position <= 0 || q.Position > 10 {
			t.Errorf("query %q at position %v is not the page-one population this measures",
				q.Key, q.Position)
		}
	}
}

// A transcription is not a measurement, and the file has to keep saying so.
// Without the flag a later comparison would read every unestablished band as
// a measured zero and report movement that was never recorded.
func TestStoredBaselineDeclaresWhatItDoesNotEstablish(t *testing.T) {
	f, err := os.Open(filepath.FromSlash(baselinePath))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	snap, err := ReadJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Partial {
		t.Error("a transcribed baseline is not marked partial")
	}
	if snap.Note == "" {
		t.Error("a partial baseline does not say what it leaves unestablished")
	}
	if !strings.Contains(snap.Source, "csx-seo-report") {
		t.Error("the baseline does not say how to regenerate itself from the export")
	}

	// And a comparison against it says so rather than inventing a movement.
	after := Build("later export", "test", []Row{
		{Key: "https://codesamplex.dev/npm/x/1.0.0/samples/a-aaaaaaaa",
			Clicks: 9, Impressions: 315, Position: 2.1},
	}, nil)
	var buf bytes.Buffer
	RenderComparison(&buf, snap, after, Compare(snap, after))
	out := buf.String()
	if !strings.Contains(out, "PARTIAL") {
		t.Error("comparison against a partial baseline does not say the baseline is partial")
	}
	if !strings.Contains(out, "not established") {
		t.Errorf("an unestablished band was compared as a measured zero:\n%s", out)
	}
	// The cohort total IS established, so it is compared for real.
	if !strings.Contains(out, "sample ") && !strings.Contains(out, "sample\n") {
		t.Errorf("the established sample cohort was not compared:\n%s", out)
	}
	// Its mean position is NOT established, though, and subtracting from an
	// unrecorded rank would put a rank collapse in the very column a reader
	// is told to check before believing a CTR movement. The "after" side
	// averages 2.10; no row may report that as a movement.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "sample") {
			continue
		}
		if strings.Contains(line, "+2.10") || strings.Contains(line, "-2.10") {
			t.Errorf("a position delta was computed against an unrecorded rank: %q", line)
		}
	}
}
