package serverstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// seedSamples writes n live samples, oldest first, so the one being looked
// for is well outside any newest-N window.
func seedSamples(t *testing.T, f *Fake, n int, targetIdx int, targetPkg, targetSeeder string) {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		pkg, seeder := "pkg:npm/filler-"+fmt.Sprint(i)+"@1.0.0", "someone-else"
		if i == targetIdx {
			pkg, seeder = targetPkg, targetSeeder
		}
		row := SampleRow{
			SampleID:     fmt.Sprintf("sha256:%064d", i),
			Status:       "PUBLISHED",
			License:      "MIT-0",
			OriginSeeder: seeder,
			CreatedAt:    base.Add(time.Duration(i) * time.Minute),
			ManifestJSON: `{"schemaVersion":1,"packages":["` + pkg + `"],` +
				`"case":{"schemaVersion":1,"kind":"HOW","goal":"g","packages":["` + pkg + `"],"contract":["c"]}}`,
		}
		if err := f.SaveSample(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
}

// The Fake is what handler tests are held to, so a query it answers from
// only the newest fifty rows cannot fail the test that would catch the
// regression Postgres was fixed for: search used to score the newest 500
// globally, which made relevance a function of publication order.
func TestFakeSamplesForPackagesSeesPastTheDefaultWindow(t *testing.T) {
	f := NewFake()
	const target = "pkg:npm/axios@1.12.0"
	// Index 0 is the OLDEST, so it sits below any newest-N window.
	seedSamples(t, f, 120, 0, target, "someone-else")

	rows, err := f.SamplesForPackages(context.Background(), []string{"pkg:npm/axios@%"}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("found %d samples naming axios, want 1 — the oldest matching "+
			"sample must still be findable once 120 exist", len(rows))
	}
}

func TestFakeSamplesBySeederSeesPastTheDefaultWindow(t *testing.T) {
	f := NewFake()
	seedSamples(t, f, 120, 0, "pkg:npm/left-pad@1.3.0", "millwright")

	rows, err := f.SamplesBySeeder(context.Background(), "millwright", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("a seeder's oldest sample vanished from their own page: got %d", len(rows))
	}
}

// The limit the caller passes must still be honoured — removing the hidden
// cap must not remove the real one.
func TestFakeSamplesForPackagesStillHonoursTheCallersLimit(t *testing.T) {
	f := NewFake()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if err := f.SaveSample(context.Background(), SampleRow{
			SampleID:  fmt.Sprintf("sha256:%064d", i),
			Status:    "PUBLISHED",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			ManifestJSON: `{"schemaVersion":1,"packages":["pkg:npm/axios@1.12.0"],` +
				`"case":{"schemaVersion":1,"kind":"HOW","goal":"g","packages":["pkg:npm/axios@1.12.0"],"contract":["c"]}}`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.SamplesForPackages(context.Background(), []string{"pkg:npm/axios@%"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Errorf("returned %d rows for a limit of 3", len(rows))
	}
}
