package main

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The package page lists versions under a heading whose empty state reads
// "No versions with evidence yet", so a listed version claims evidence
// exists for it. The list came from the packages table — which the
// publicness gate also writes to, including purls whose evidence batch was
// then refused — while the version page 404s unless that exact version has
// a snapshot target.
//
// Every such row was therefore a link into a 404, on the page a crawler
// reaches first.
func TestOnlyVersionsWithAPageAreListed(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()

	for _, v := range []string{"1.12.0", "1.11.0"} {
		if err := store.UpsertPackage(ctx, serverstore.PackageRow{
			PURL: "pkg:npm/axios@" + v, Ecosystem: "npm", Name: "axios",
			Version: v, Major: "1", Publicness: "PUBLIC",
			FirstSeen: time.Now(), LastSeen: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Only 1.12.0 has anything behind it: a package-level snapshot, which
	// is one of the two things versionPage renders from.
	if err := store.PutSnapshot(ctx, "pkg:npm/axios@1.12.0", "", `{"rows":[]}`); err != nil {
		t.Fatal(err)
	}

	w := &webStore{s: store}
	got, err := w.PackageVersions(ctx, "npm", "axios")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got {
		if v == "1.11.0" {
			t.Errorf("listed 1.11.0, whose page is a 404: %v", got)
		}
	}
	var found bool
	for _, v := range got {
		if v == "1.12.0" {
			found = true
		}
	}
	if !found {
		t.Errorf("dropped 1.12.0, which does have a page: %v", got)
	}
}

func TestPackageVersionsLatestUsesVersionNotLastSeen(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	now := time.Now()
	for _, row := range []serverstore.PackageRow{
		{
			PURL: "pkg:cargo/tokio@1.43.0", Ecosystem: "cargo", Name: "tokio",
			Version: "1.43.0", Major: "1", Publicness: "PUBLIC",
			FirstSeen: now.Add(-time.Hour), LastSeen: now,
		},
		{
			PURL: "pkg:cargo/tokio@1.53.1", Ecosystem: "cargo", Name: "tokio",
			Version: "1.53.1", Major: "1", Publicness: "PUBLIC",
			FirstSeen: now.Add(-time.Hour), LastSeen: now.Add(-time.Hour),
		},
	} {
		if err := store.UpsertPackage(ctx, row); err != nil {
			t.Fatal(err)
		}
		if err := store.PutSnapshot(ctx, row.PURL, "", `{"rows":[]}`); err != nil {
			t.Fatal(err)
		}
	}

	got, err := (&webStore{s: store}).PackageVersions(ctx, "cargo", "tokio")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "1.53.1" || got[1] != "1.43.0" {
		t.Fatalf("versions = %v, want latest release first despite older evidence", got)
	}
}
