// Package seoreport turns a Google Search Console export into the one
// comparison this project needs: did the pages that were already ranking
// start getting clicked.
//
// On 2026-08-27 the export said 187 /samples/sha256:* pages had 1,546
// impressions and 0 clicks, and that 157 of them averaged inside the first
// ten results, carrying 1,393 of those impressions. Google was showing the
// pages; nobody clicked. That is a snippet problem, not a ranking problem,
// and the two are only distinguishable if they are measured apart — which
// is what this package exists to do.
//
// Two things it deliberately does NOT do.
//
// It does not average CTR across position bands. A page that falls from
// position 4 to position 12 loses clicks for a reason that has nothing to
// do with its title, and a change that improved every snippet would still
// read as a regression. So every comparison is per band.
//
// It does not treat a URL shape as a cohort. Sample pages now answer at a
// human-readable address as well as at their content address, and both are
// the same page — so the cohort is "sample pages", matched on either shape.
// Splitting them would show the sample cohort collapsing to zero
// impressions on the day the canonical moved.
package seoreport

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// PageClass is which part of the site a URL belongs to. The question the
// ticket asks — do sample pages convert impressions worse than package
// pages — is a question about these classes.
type PageClass string

const (
	// ClassSample is a published sample, at either of its two addresses:
	// the content address /samples/sha256:<digest>, and the human-readable
	// /{ecosystem}/{name}/{version}/samples/{slug}.
	ClassSample PageClass = "sample"
	// ClassPackage is a package, release or API page in the explorer.
	ClassPackage PageClass = "package"
	// ClassSite is everything else: the landing cluster, /findings,
	// /records, /wanted, /features.
	ClassSite PageClass = "site"
)

// knownEcosystems is the explorer's routed namespace. A path whose first
// segment is not one of these is not a package page, and guessing that it
// is would file the landing translations under "package".
//
// It is duplicated from internal/web rather than imported: this is an
// offline reporting tool and it must not drag the website's template and
// asset embedding into an operator's `go run`.
var knownEcosystems = map[string]bool{
	"npm": true, "pypi": true, "cargo": true, "golang": true,
	"maven": true, "gem": true, "composer": true, "hex": true, "pub": true,
}

// Classify decides which cohort a Search Console page URL belongs to.
func Classify(rawURL string) PageClass {
	path := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		path = u.Path
	}
	path = "/" + strings.Trim(path, "/")
	segs := strings.Split(strings.Trim(path, "/"), "/")
	// The content address: /samples/<id>. The bare /samples collection is
	// not a sample page — it is a section index.
	if len(segs) >= 2 && segs[0] == "samples" {
		return ClassSample
	}
	if len(segs) == 0 || segs[0] == "" || !knownEcosystems[segs[0]] {
		return ClassSite
	}
	// The human-readable address ends /samples/<slug> under a release.
	if len(segs) >= 4 && segs[len(segs)-2] == "samples" {
		return ClassSample
	}
	return ClassPackage
}

// Band is a position band. Average position is itself an average, so a band
// is an approximation — but it is the approximation that keeps a ranking
// change from being read as a snippet change, which is the only thing that
// matters here.
type Band string

const (
	Band1to3   Band = "1-3"
	Band4to10  Band = "4-10"
	Band11to20 Band = "11-20"
	Band21plus Band = "21+"
)

// Bands lists the bands in reading order.
var Bands = []Band{Band1to3, Band4to10, Band11to20, Band21plus}

// BandOf places an average position in its band. A row with no position at
// all lands in the last band rather than the first: an unknown rank is not
// evidence of a good one.
func BandOf(position float64) Band {
	switch {
	case position >= 1 && position <= 3:
		return Band1to3
	case position > 3 && position <= 10:
		return Band4to10
	case position > 10 && position <= 20:
		return Band11to20
	}
	return Band21plus
}

// Row is one line of a Search Console export: a page or a query, with what
// it did.
type Row struct {
	Key         string  `json:"key"`
	Clicks      int64   `json:"clicks"`
	Impressions int64   `json:"impressions"`
	Position    float64 `json:"position"`
}

// Totals is what a cohort did.
type Totals struct {
	Pages       int   `json:"pages"`
	Clicks      int64 `json:"clicks"`
	Impressions int64 `json:"impressions"`
	// CTR is clicks/impressions as a percentage, rounded to two decimals.
	// It is zero when there were no impressions — not "unknown dressed as
	// zero": Impressions is right there beside it, and a reader can see the
	// denominator that produced it.
	CTR float64 `json:"ctr"`
	// MeanPosition is impression-weighted. An unweighted mean lets a page
	// with two impressions at rank 90 move the number as much as one with
	// nine hundred at rank 5.
	MeanPosition float64 `json:"meanPosition"`
}

func (t *Totals) add(r Row) {
	t.Pages++
	t.Clicks += r.Clicks
	t.Impressions += r.Impressions
	t.MeanPosition += r.Position * float64(r.Impressions)
}

func (t *Totals) finish() {
	if t.Impressions > 0 {
		t.CTR = round2(100 * float64(t.Clicks) / float64(t.Impressions))
		t.MeanPosition = round2(t.MeanPosition / float64(t.Impressions))
		return
	}
	t.MeanPosition = 0
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// Cohort is one page class, whole and broken down by position band.
type Cohort struct {
	Class PageClass       `json:"class"`
	All   Totals          `json:"all"`
	Bands map[Band]Totals `json:"bands"`
	// Top is the highest-impression pages of the cohort, so a report can be
	// read against the specific pages a change was made for.
	Top []Row `json:"top,omitempty"`
}

// Snapshot is one measurement of the whole site: every cohort, plus the
// queries, as of one export.
type Snapshot struct {
	// Label is the export this was taken from, in the operator's words.
	Label string `json:"label"`
	// Source records where the numbers came from. A snapshot whose numbers
	// were transcribed rather than parsed has to say so, or a later reader
	// cannot tell a measurement from a quotation.
	Source  string               `json:"source"`
	Cohorts map[PageClass]Cohort `json:"cohorts"`
	Queries []Row                `json:"queries,omitempty"`
	// SampleTop10 is the sample cohort restricted to pages averaging inside
	// the first ten results. It is called out because it is the population
	// the whole ticket is about: pages Google already put on page one.
	SampleTop10 Totals `json:"sampleTop10"`
	// Partial marks a snapshot that was written from a report rather than
	// parsed from an export, so some of it is simply not established.
	//
	// The pre-deploy baseline is one: the export was read by a person, who
	// recorded the cohort totals and the page-one population and did not
	// record the 1-3 against 4-10 split. A zero in a band of a partial
	// snapshot means "not established", and a comparison that let it stand
	// as a measured zero would report every band as having appeared out of
	// nothing.
	Partial bool `json:"partial,omitempty"`
	// Note carries whatever the person who wrote a partial snapshot needs a
	// later reader to know.
	Note string `json:"note,omitempty"`
}

// topRowLimit bounds the per-cohort page list a report carries.
const topRowLimit = 25

// queryRowLimit bounds the query list.
const queryRowLimit = 40

// Build assembles a snapshot from parsed page and query rows.
func Build(label, source string, pages, queries []Row) Snapshot {
	snap := Snapshot{
		Label: label, Source: source,
		Cohorts: map[PageClass]Cohort{},
	}
	byClass := map[PageClass][]Row{}
	for _, r := range pages {
		class := Classify(r.Key)
		byClass[class] = append(byClass[class], r)
	}
	for _, class := range []PageClass{ClassSample, ClassPackage, ClassSite} {
		rows := byClass[class]
		cohort := Cohort{Class: class, Bands: map[Band]Totals{}}
		bands := map[Band]*Totals{}
		for _, band := range Bands {
			bands[band] = &Totals{}
		}
		for _, r := range rows {
			cohort.All.add(r)
			bands[BandOf(r.Position)].add(r)
			if class == ClassSample && r.Position > 0 && r.Position <= 10 {
				snap.SampleTop10.add(r)
			}
		}
		cohort.All.finish()
		for _, band := range Bands {
			t := bands[band]
			t.finish()
			cohort.Bands[band] = *t
		}
		sorted := append([]Row(nil), rows...)
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].Impressions > sorted[j].Impressions
		})
		if len(sorted) > topRowLimit {
			sorted = sorted[:topRowLimit]
		}
		cohort.Top = sorted
		snap.Cohorts[class] = cohort
	}
	snap.SampleTop10.finish()

	sortedQueries := append([]Row(nil), queries...)
	sort.SliceStable(sortedQueries, func(i, j int) bool {
		return sortedQueries[i].Impressions > sortedQueries[j].Impressions
	})
	if len(sortedQueries) > queryRowLimit {
		sortedQueries = sortedQueries[:queryRowLimit]
	}
	snap.Queries = sortedQueries
	return snap
}

// ---------------------------------------------------------------------------
// Reading the export.

// ParseCSV reads one Search Console CSV export.
//
// The columns are found by header name rather than by position, because the
// export is written in the console's display language and the column ORDER
// has never been part of any contract. The first column is the key — the
// page URL or the query — whatever it happens to be called.
func ParseCSV(r io.Reader) ([]Row, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("csv has no data rows")
	}
	header := records[0]
	// A UTF-8 BOM survives into the first header cell and would make the
	// column-name match fail on the one column that is always present.
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff")
	}
	clicks, impressions, position := -1, -1, -1
	for i, h := range header {
		switch lower := strings.ToLower(strings.TrimSpace(h)); {
		case strings.Contains(lower, "click"):
			clicks = i
		case strings.Contains(lower, "impression"):
			impressions = i
		case strings.Contains(lower, "position"):
			position = i
		}
	}
	if clicks < 0 || impressions < 0 {
		return nil, fmt.Errorf("csv header names no clicks/impressions column: %v", header)
	}
	out := make([]Row, 0, len(records)-1)
	for _, rec := range records[1:] {
		if len(rec) == 0 || strings.TrimSpace(rec[0]) == "" {
			continue
		}
		row := Row{Key: strings.TrimSpace(rec[0])}
		row.Clicks = parseInt(field(rec, clicks))
		row.Impressions = parseInt(field(rec, impressions))
		row.Position = parseFloat(field(rec, position))
		out = append(out, row)
	}
	return out, nil
}

func field(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return rec[i]
}

// parseInt reads a count as the export writes it, thousands separators and
// all. An unreadable count is zero: Search Console never omits one, so a
// value this cannot read is a column that is not a count.
func parseInt(s string) int64 {
	s = strings.NewReplacer(",", "", " ", "", " ", "").Replace(strings.TrimSpace(s))
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseFloat reads an average position. Locales that write "6,95" are read
// the same as "6.95" — but only when the string holds no other separator,
// so "1,234.5" is not turned into 1.2345.
func parseFloat(s string) float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, " ", ""))
	if s == "" {
		return 0
	}
	if !strings.Contains(s, ".") && strings.Count(s, ",") == 1 {
		s = strings.Replace(s, ",", ".", 1)
	}
	s = strings.ReplaceAll(s, ",", "")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// ---------------------------------------------------------------------------
// Comparison.

// Delta is one measured movement between two snapshots.
type Delta struct {
	Scope string `json:"scope"`
	// Before and After are the two sides, so a reader never has to trust the
	// difference without its operands.
	Before Totals `json:"before"`
	After  Totals `json:"after"`
	// CTRPoints is the change in CTR in percentage POINTS, not percent. A
	// CTR going 0.00% → 1.20% is +1.20 points; calling it "+infinity
	// percent" is how a zero baseline gets reported as a triumph.
	CTRPoints float64 `json:"ctrPoints"`
	ClickDiff int64   `json:"clickDiff"`
	// ImpressionDiff and PositionDiff are what says whether a CTR change is
	// a snippet result at all. A cohort that lost half its impressions and
	// moved from rank 12 to rank 4 has not proven anything about its titles.
	ImpressionDiff int64   `json:"impressionDiff"`
	PositionDiff   float64 `json:"positionDiff"`
}

func delta(scope string, before, after Totals) Delta {
	return Delta{
		Scope: scope, Before: before, After: after,
		CTRPoints:      round2(after.CTR - before.CTR),
		ClickDiff:      after.Clicks - before.Clicks,
		ImpressionDiff: after.Impressions - before.Impressions,
		PositionDiff:   round2(after.MeanPosition - before.MeanPosition),
	}
}

// Compare measures the movement of every cohort and every band.
func Compare(before, after Snapshot) []Delta {
	var out []Delta
	for _, class := range []PageClass{ClassSample, ClassPackage, ClassSite} {
		b, a := before.Cohorts[class], after.Cohorts[class]
		out = append(out, delta(string(class), b.All, a.All))
		for _, band := range Bands {
			out = append(out, delta(string(class)+" "+string(band), b.Bands[band], a.Bands[band]))
		}
	}
	out = append(out, delta("sample position<=10", before.SampleTop10, after.SampleTop10))
	return out
}

// ---------------------------------------------------------------------------
// Rendering.

// WriteJSON writes a snapshot as the stored baseline format.
func WriteJSON(w io.Writer, snap Snapshot) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(snap)
}

// ReadJSON reads a stored baseline.
func ReadJSON(r io.Reader) (Snapshot, error) {
	var snap Snapshot
	if err := json.NewDecoder(r).Decode(&snap); err != nil {
		return Snapshot{}, fmt.Errorf("read baseline: %w", err)
	}
	return snap, nil
}

// RenderSnapshot writes the human-readable form of one measurement.
func RenderSnapshot(w io.Writer, snap Snapshot) {
	fmt.Fprintf(w, "%s\n", snap.Label)
	if snap.Source != "" {
		fmt.Fprintf(w, "source: %s\n", snap.Source)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%-22s %7s %7s %12s %7s %9s\n",
		"cohort", "pages", "clicks", "impressions", "ctr%", "position")
	for _, class := range []PageClass{ClassSample, ClassPackage, ClassSite} {
		c := snap.Cohorts[class]
		writeTotals(w, string(class), c.All)
		for _, band := range Bands {
			t := c.Bands[band]
			if t.Pages == 0 {
				continue
			}
			writeTotals(w, "  "+string(band), t)
		}
	}
	writeTotals(w, "sample position<=10", snap.SampleTop10)
}

func writeTotals(w io.Writer, label string, t Totals) {
	fmt.Fprintf(w, "%-22s %7d %7d %12d %7.2f %9.2f\n",
		label, t.Pages, t.Clicks, t.Impressions, t.CTR, t.MeanPosition)
}

// RenderComparison writes the before/after table.
func RenderComparison(w io.Writer, before, after Snapshot, deltas []Delta) {
	fmt.Fprintf(w, "before: %s\nafter:  %s\n\n", before.Label, after.Label)
	if before.Partial {
		fmt.Fprintln(w, "The baseline is PARTIAL: it was transcribed, not parsed.")
		if before.Note != "" {
			fmt.Fprintln(w, before.Note)
		}
		fmt.Fprintln(w, "Rows it does not establish are omitted rather than compared against zero.")
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%-22s %17s %17s %9s %8s %13s %10s\n",
		"scope", "ctr% before", "ctr% after", "ctr pts", "clicks", "impressions", "position")
	for _, d := range deltas {
		if d.Before.Impressions == 0 && d.After.Impressions == 0 {
			continue
		}
		// A zero on the "before" side of a partial baseline is an absence of
		// a measurement, and subtracting from it would manufacture a
		// movement the baseline never recorded.
		if before.Partial && d.Before.Impressions == 0 && d.Before.Pages == 0 {
			fmt.Fprintf(w, "%-22s %17s %17.2f %9s %+8d %+13s %10s\n",
				d.Scope, "not established", d.After.CTR, "-",
				d.After.Clicks, "-", "-")
			continue
		}
		// A cohort total can be established while its mean position is not:
		// the transcribed baseline records how many impressions the sample
		// pages took and not what rank they averaged. Printing the
		// subtraction anyway would report a rank collapse that was never
		// measured, in the one column a reader is told to check first.
		position := fmt.Sprintf("%+10.2f", d.PositionDiff)
		if before.Partial && d.Before.MeanPosition == 0 {
			position = fmt.Sprintf("%10s", "-")
		}
		fmt.Fprintf(w, "%-22s %17.2f %17.2f %+9.2f %+8d %+13d %s\n",
			d.Scope, d.Before.CTR, d.After.CTR, d.CTRPoints,
			d.ClickDiff, d.ImpressionDiff, position)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A CTR change is only a snippet result when impressions and")
	fmt.Fprintln(w, "position held. Read the last two columns before the third.")
}
