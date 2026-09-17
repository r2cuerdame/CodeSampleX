package serverstore

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// playAdminDemand runs one demand script through a store: two live samples
// carrying axios (one quarantined and therefore not counted), one package
// with no sample, wanted reports inside, before and outside the window,
// and search hits on three days.
func playAdminDemand(t *testing.T, store interface {
	UpsertPackage(context.Context, PackageRow) error
	SaveSample(context.Context, SampleRow) error
	RecordWantedBatch(context.Context, []WantedSubmission) error
	RecordSearchHit(context.Context, SearchHitRow) error
	AdminDemandReader
}, now time.Time) AdminDemand {
	t.Helper()
	ctx := context.Background()
	for _, pkg := range []PackageRow{
		{PURL: "pkg:npm/axios@1.12.0", Ecosystem: "npm", Name: "axios", Version: "1.12.0", Major: "1", Publicness: "PUBLIC"},
		{PURL: "pkg:pypi/requests@2.32.3", Ecosystem: "pypi", Name: "requests", Version: "2.32.3", Major: "2", Publicness: "PUBLIC"},
	} {
		if err := store.UpsertPackage(ctx, pkg); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"packages":["pkg:npm/axios@1.12.0"],"goal":"get","symbols":["axios.get"]}`
	for i, sample := range []SampleRow{
		{SampleID: "sha256:" + repeatHex("a1", 32), ManifestJSON: manifest, SizeBytes: 10},
		{SampleID: "sha256:" + repeatHex("a2", 32), ManifestJSON: manifest, SizeBytes: 10},
		{SampleID: "sha256:" + repeatHex("a3", 32), ManifestJSON: manifest, SizeBytes: 10, Quarantined: true, QuarantineReason: "withdrawn"},
	} {
		sample.CreatedAt = now.Add(-time.Duration(i) * time.Hour)
		if err := store.SaveSample(ctx, sample); err != nil {
			t.Fatal(err)
		}
	}
	day := func(offset int) string { return now.AddDate(0, 0, -offset).Format("2006-01-02") }
	reports := []WantedSubmission{
		{Epoch: day(0), AnonID: "a", Rows: []WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post"}, {Ecosystem: "pypi", Name: "requests", Symbol: "Session.get"}}},
		{Epoch: day(1), AnonID: "b", Rows: []WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post"}}},
		{Epoch: day(1), AnonID: "b", Rows: []WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post"}}},
		{Epoch: day(2), AnonID: "c", Rows: []WantedRow{{Ecosystem: "pypi", Name: "requests", Symbol: "Session.get"}}},
		{Epoch: day(6), AnonID: "d", Rows: []WantedRow{{Ecosystem: "pypi", Name: "requests", Symbol: "Session.post", TargetOS: "linux"}}},
		{Epoch: day(8), AnonID: "e", Rows: []WantedRow{{Ecosystem: "pypi", Name: "requests", Symbol: "Session.get"}}},
		{Epoch: day(9), AnonID: "f", Rows: []WantedRow{{Ecosystem: "cargo", Name: "old", Version: "1.0.0"}}},
		{Epoch: day(20), AnonID: "g", Rows: []WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post"}}},
	}
	if err := store.RecordWantedBatch(ctx, reports); err != nil {
		t.Fatal(err)
	}
	for _, hit := range []SearchHitRow{
		{Grade: "VERIFIED", ResultsShown: 1, SampleID: "sha256:" + repeatHex("a1", 32), OfferID: "o1", Epoch: day(0), AnonID: "a"},
		{Grade: "VERIFIED", ResultsShown: 1, SampleID: "sha256:" + repeatHex("a1", 32), OfferID: "o1", Epoch: day(0), AnonID: "a"},
		{Grade: "VERIFIED", ResultsShown: 2, SampleID: "sha256:" + repeatHex("a2", 32), OfferID: "o2", Epoch: day(3), AnonID: "b"},
		{Grade: "VERIFIED", ResultsShown: 2, SampleID: "sha256:" + repeatHex("a2", 32), OfferID: "o3", Epoch: day(10), AnonID: "b"},
	} {
		if err := store.RecordSearchHit(ctx, hit); err != nil {
			t.Fatal(err)
		}
	}
	out, err := store.AdminDemand(ctx, now)
	if err != nil {
		t.Fatalf("AdminDemand: %v", err)
	}
	return out
}

func repeatHex(pair string, n int) string {
	out := make([]byte, 0, len(pair)*n)
	for i := 0; i < n; i++ {
		out = append(out, pair...)
	}
	return string(out)
}

func TestFakeAdminDemandShape(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	got := playAdminDemand(t, NewFake(), now)
	if got.WindowStart != "2026-09-11" || got.WindowEnd != "2026-09-17" || got.TotalImpact != 5 || got.PackageCount != 2 {
		t.Fatalf("window/totals = %+v", got)
	}
	if len(got.Packages) != 2 || got.Packages[0].Name != "requests" || got.Packages[0].Impact != 3 || got.Packages[0].PriorImpact != 1 || got.Packages[0].Coordinates != 2 || got.Packages[0].Samples != 0 || got.Packages[0].LastDay != "2026-09-17" {
		t.Fatalf("packages = %+v", got.Packages)
	}
	if got.Packages[1].Name != "axios" || got.Packages[1].Impact != 2 || got.Packages[1].PriorImpact != 0 || got.Packages[1].Samples != 2 || got.Packages[1].Coordinates != 1 {
		t.Fatalf("axios = %+v", got.Packages[1])
	}
	if len(got.Gaps) != 2 || got.Gaps[0].Name != "requests" {
		t.Fatalf("gaps = %+v", got.Gaps)
	}
	if len(got.TopMisses) != 3 || got.TopMisses[0].Symbol != "axios.post" || got.TopMisses[0].Impact != 2 || got.TopMisses[1].Symbol != "Session.get" || got.TopMisses[2].TargetOS != "linux" {
		t.Fatalf("top misses = %+v", got.TopMisses)
	}
	if len(got.Search) != AdminDemandSeriesDays || got.Search[13].Day != "2026-09-17" || got.Search[13].Hits != 1 || got.Search[13].Misses != 1 || got.Search[12].Misses != 1 || got.Search[3].Hits != 1 {
		t.Fatalf("search = %+v", got.Search)
	}
	if got.Week != (AdminDemandSearchWindow{Hits: 2, Misses: 4}) || got.PriorWeek != (AdminDemandSearchWindow{Hits: 1, Misses: 2}) {
		t.Fatalf("week=%+v prior=%+v", got.Week, got.PriorWeek)
	}
}

func TestIntegrationAdminDemandParity(t *testing.T) {
	pg := openTestPG(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := playAdminDemand(t, NewFake(), now)
	got := playAdminDemand(t, pg, now)
	if !reflect.DeepEqual(fake, got) {
		t.Fatalf("admin demand parity\nfake=%+v\n  pg=%+v", fake, got)
	}
}
