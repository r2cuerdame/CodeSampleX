package seoreport

import (
	"bytes"
	"strings"
	"testing"
)

// The cohort is the page, not the address it was reached at. Sample pages
// answer at two URLs now, and a cohort matched on one shape would show the
// sample population collapsing to nothing on the day the canonical moved.
func TestBothSampleAddressShapesAreTheSameCohort(t *testing.T) {
	cases := []struct {
		url  string
		want PageClass
	}{
		{"https://codesamplex.dev/samples/sha256:5a2468d2cc16", ClassSample},
		{"https://codesamplex.dev/npm/browserslist/4.28.7/samples/parseconfig-5a2468d2", ClassSample},
		{"https://codesamplex.dev/golang/github.com/jackc/pgx/v5/v5.10.0/samples/parseconfig-aabbccdd", ClassSample},
		{"https://codesamplex.dev/npm/browserslist", ClassPackage},
		{"https://codesamplex.dev/npm/browserslist/4.28.7", ClassPackage},
		{"https://codesamplex.dev/npm/axios/1.12.0/axios.post", ClassPackage},
		{"https://codesamplex.dev/", ClassSite},
		{"https://codesamplex.dev/findings", ClassSite},
		{"https://codesamplex.dev/ko/", ClassSite},
		// A section index is not a sample page.
		{"https://codesamplex.dev/samples", ClassSite},
		// An unrouted first segment is not a package, however much it looks
		// like one.
		{"https://codesamplex.dev/nuget/Newtonsoft.Json", ClassSite},
	}
	for _, tc := range cases {
		if got := Classify(tc.url); got != tc.want {
			t.Errorf("Classify(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestBandOfPutsAnUnknownRankLast(t *testing.T) {
	cases := []struct {
		position float64
		want     Band
	}{
		{1, Band1to3}, {3, Band1to3}, {3.01, Band4to10},
		{5.31, Band4to10}, {10, Band4to10}, {10.4, Band11to20},
		{20, Band11to20}, {20.1, Band21plus}, {96, Band21plus},
		// No position recorded is not position one.
		{0, Band21plus},
	}
	for _, tc := range cases {
		if got := BandOf(tc.position); got != tc.want {
			t.Errorf("BandOf(%v) = %q, want %q", tc.position, got, tc.want)
		}
	}
}

// The 2026-08-27 export, in miniature: sample pages ranking on page one and
// converting nothing.
func baselineRows() []Row {
	return []Row{
		{Key: "https://codesamplex.dev/samples/sha256:aa", Clicks: 0, Impressions: 315, Position: 6.95},
		{Key: "https://codesamplex.dev/samples/sha256:bb", Clicks: 0, Impressions: 229, Position: 5.31},
		{Key: "https://codesamplex.dev/samples/sha256:cc", Clicks: 0, Impressions: 105, Position: 6.49},
		{Key: "https://codesamplex.dev/samples/sha256:dd", Clicks: 0, Impressions: 40, Position: 14.2},
		{Key: "https://codesamplex.dev/npm/nanoid", Clicks: 4, Impressions: 120, Position: 7.29},
		{Key: "https://codesamplex.dev/", Clicks: 3, Impressions: 60, Position: 12.0},
	}
}

func TestSnapshotSeparatesTheCohortsAndTheBands(t *testing.T) {
	snap := Build("2026-08-27 export", "test", baselineRows(), nil)

	sample := snap.Cohorts[ClassSample]
	if sample.All.Pages != 4 || sample.All.Impressions != 689 || sample.All.Clicks != 0 {
		t.Fatalf("sample cohort = %+v", sample.All)
	}
	if sample.All.CTR != 0 {
		t.Errorf("sample CTR = %v, want 0", sample.All.CTR)
	}
	// Three of the four rank on page one, and they carry 649 of the 689
	// impressions. That population is the whole point of the exercise.
	if snap.SampleTop10.Pages != 3 || snap.SampleTop10.Impressions != 649 {
		t.Errorf("sample top-10 = %+v", snap.SampleTop10)
	}
	if got := sample.Bands[Band11to20]; got.Pages != 1 || got.Impressions != 40 {
		t.Errorf("11-20 band = %+v", got)
	}
	if got := sample.Bands[Band1to3]; got.Pages != 0 {
		t.Errorf("1-3 band = %+v, want empty", got)
	}
	pkg := snap.Cohorts[ClassPackage]
	if pkg.All.Pages != 1 || pkg.All.CTR != 3.33 {
		t.Errorf("package cohort = %+v", pkg.All)
	}
	// Impression-weighted: the 315-impression page at 6.95 has to weigh
	// more than the 40-impression page at 14.2.
	if sample.All.MeanPosition > 7 {
		t.Errorf("mean position = %v, want it pulled toward the big pages",
			sample.All.MeanPosition)
	}
}

// A CTR that improved because the pages fell out of the index is not a CTR
// improvement, so the comparison carries the operands and the two columns
// that say whether the comparison means anything.
func TestComparisonReportsCTRInPointsBesideItsCause(t *testing.T) {
	before := Build("before", "test", baselineRows(), nil)
	after := Build("after", "test", []Row{
		{Key: "https://codesamplex.dev/npm/x/1.0.0/samples/a-aaaaaaaa", Clicks: 8, Impressions: 315, Position: 6.95},
		{Key: "https://codesamplex.dev/npm/x/1.0.0/samples/b-bbbbbbbb", Clicks: 5, Impressions: 229, Position: 5.31},
		{Key: "https://codesamplex.dev/npm/x/1.0.0/samples/c-cccccccc", Clicks: 2, Impressions: 105, Position: 6.49},
		{Key: "https://codesamplex.dev/npm/x/1.0.0/samples/d-dddddddd", Clicks: 0, Impressions: 40, Position: 14.2},
		{Key: "https://codesamplex.dev/npm/nanoid", Clicks: 4, Impressions: 120, Position: 7.29},
		{Key: "https://codesamplex.dev/", Clicks: 3, Impressions: 60, Position: 12.0},
	}, nil)

	deltas := Compare(before, after)
	var sample *Delta
	for i := range deltas {
		if deltas[i].Scope == string(ClassSample) {
			sample = &deltas[i]
		}
	}
	if sample == nil {
		t.Fatal("no sample cohort delta")
	}
	if sample.ClickDiff != 15 {
		t.Errorf("clickDiff = %d, want 15", sample.ClickDiff)
	}
	// Points, not percent. 0.00% to 2.18% is +2.18 points; expressing it as
	// a ratio against a zero baseline is how a division by zero gets
	// published as a result.
	if sample.CTRPoints != round2(sample.After.CTR) {
		t.Errorf("ctrPoints = %v, want %v", sample.CTRPoints, sample.After.CTR)
	}
	// Impressions and position held, so the movement is attributable.
	if sample.ImpressionDiff != 0 {
		t.Errorf("impressionDiff = %d, want 0", sample.ImpressionDiff)
	}
	if sample.PositionDiff != 0 {
		t.Errorf("positionDiff = %v, want 0", sample.PositionDiff)
	}

	var buf bytes.Buffer
	RenderComparison(&buf, before, after, deltas)
	out := buf.String()
	for _, want := range []string{"ctr% before", "ctr% after", "impressions", "position"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered comparison missing %q", want)
		}
	}
}

// The export is written in the console's display language, so columns are
// found by name and numbers are read in the shapes the console writes them.
func TestParseCSVFindsColumnsByNameNotPosition(t *testing.T) {
	csv := "\ufeffTop pages,Position,Impressions,CTR,Clicks\n" +
		"https://codesamplex.dev/samples/sha256:aa,6.95,\"1,546\",0%,0\n" +
		"https://codesamplex.dev/npm/nanoid,7.29,120,3.33%,4\n"
	rows, err := ParseCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Impressions != 1546 || rows[0].Clicks != 0 || rows[0].Position != 6.95 {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Clicks != 4 {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

func TestParseCSVReadsACommaDecimalWithoutInventingAThousand(t *testing.T) {
	csv := "Top pages,Clicks,Impressions,Position\n" +
		"https://codesamplex.dev/a,0,10,\"6,95\"\n"
	rows, err := ParseCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Position != 6.95 {
		t.Errorf("position = %v, want 6.95", rows[0].Position)
	}
}

func TestParseCSVRefusesAFileWithNoCountColumns(t *testing.T) {
	if _, err := ParseCSV(strings.NewReader("a,b\n1,2\n")); err == nil {
		t.Error("a file with no clicks or impressions column parsed as an export")
	}
}

// A baseline has to survive the round trip it exists for.
func TestBaselineRoundTrips(t *testing.T) {
	snap := Build("2026-08-27 export", "transcribed", baselineRows(), []Row{
		{Key: "nanoid npm", Clicks: 0, Impressions: 56, Position: 7.29},
	})
	var buf bytes.Buffer
	if err := WriteJSON(&buf, snap); err != nil {
		t.Fatal(err)
	}
	back, err := ReadJSON(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if back.Cohorts[ClassSample].All != snap.Cohorts[ClassSample].All {
		t.Errorf("sample cohort did not round trip: %+v vs %+v",
			back.Cohorts[ClassSample].All, snap.Cohorts[ClassSample].All)
	}
	if back.SampleTop10 != snap.SampleTop10 {
		t.Errorf("top-10 population did not round trip")
	}
	if len(back.Queries) != 1 || back.Queries[0].Key != "nanoid npm" {
		t.Errorf("queries did not round trip: %+v", back.Queries)
	}
	if back.Source != "transcribed" {
		t.Errorf("a transcribed baseline lost the fact that it was transcribed")
	}
}
