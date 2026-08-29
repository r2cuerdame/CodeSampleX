package main

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func releaseManifest(purl, goal string) string {
	return `{
		"schemaVersion":1,
		"packages":["` + purl + `"],
		"symbols":["parseConfig"],
		"case":{"schemaVersion":1,"kind":"HOW","goal":"` + goal + `",
			"packages":["` + purl + `"],
			"contract":["it parses"]}}
	`
}

// ReleaseSamples is what turns a human-readable sample URL back into a
// content address, so anything it returns for the wrong coordinate becomes
// a page served at somebody else's address.
//
// The store matches with a SQL LIKE pattern where "_" is a wildcard and a
// prefix is not an equality, so "@1.12.0" would otherwise answer for
// "@1.12.01" and "typing_extensions" for "typing-extensions".
func TestReleaseSamplesResolvesOneExactRelease(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	w := &webStore{s: store}

	rows := []struct {
		id, purl string
	}{
		{"sha256:wanted", "pkg:npm/axios@1.12.0"},
		// A longer version that starts the same way. A prefix match takes
		// it; the coordinate is a different release.
		{"sha256:longer", "pkg:npm/axios@1.12.01"},
		{"sha256:other", "pkg:npm/axios@1.13.0"},
		// "_" is a LIKE wildcard, so an underscore in a package name would
		// match a hyphen in a different package.
		{"sha256:underscored", "pkg:pypi/typing_extensions@4.15.0"},
		{"sha256:hyphenated", "pkg:pypi/typing-extensions@4.15.0"},
	}
	for _, r := range rows {
		if err := store.SaveSample(ctx, serverstore.SampleRow{
			SampleID: r.id, ManifestJSON: releaseManifest(r.purl, "answer for "+r.purl),
			Status: "PUBLISHED",
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := w.ReleaseSamples(ctx, "npm", "axios", "1.12.0", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SampleID != "sha256:wanted" {
		t.Fatalf("axios@1.12.0 resolved to %+v, want only sha256:wanted", got)
	}
	// The row carries the whole coordinate, which is what lets a list row
	// and the sitemap name the sample's canonical URL without opening it.
	if got[0].Ecosystem != "npm" || got[0].Name != "axios" || got[0].Version != "1.12.0" {
		t.Errorf("release coordinate = %q/%q@%q", got[0].Ecosystem, got[0].Name, got[0].Version)
	}

	under, err := w.ReleaseSamples(ctx, "pypi", "typing_extensions", "4.15.0", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(under) != 1 || under[0].SampleID != "sha256:underscored" {
		t.Fatalf("typing_extensions resolved to %+v, want only sha256:underscored", under)
	}

	// An empty version is not "any version": it is a caller that has no
	// release, and a release lookup with no release resolves to nothing.
	if none, err := w.ReleaseSamples(ctx, "npm", "axios", "", 100); err != nil || len(none) != 0 {
		t.Errorf("empty version returned %+v (err=%v)", none, err)
	}
}

// A readable URL is addressing, not a claim. Verification is a separate
// fact the page states for itself — so a published sample that nothing has
// run still resolves, and still reaches a page that says so.
func TestReleaseSamplesDoesNotRequireAContractPass(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	w := &webStore{s: store}
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     "sha256:sourceonly",
		ManifestJSON: releaseManifest("pkg:npm/axios@1.12.0", "source only"),
		Status:       "PUBLISHED",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := w.ReleaseSamples(ctx, "npm", "axios", "1.12.0", 100)
	if err != nil || len(got) != 1 {
		t.Fatalf("a source-only sample did not resolve at its own address: %+v (err=%v)", got, err)
	}
	// PackageSamples is the display list and it does require the receipt.
	// The two answer different questions and must not be merged.
	listed, err := w.PackageSamples(ctx, "npm", "axios", 25)
	if err != nil || len(listed) != 0 {
		t.Fatalf("a source-only sample reached the verified display list: %+v (err=%v)", listed, err)
	}
}

// A quarantined sample must not be reachable at any address.
func TestReleaseSamplesExcludesQuarantined(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	w := &webStore{s: store}
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     "sha256:hidden",
		ManifestJSON: releaseManifest("pkg:npm/axios@1.12.0", "hidden"),
		Status:       "PUBLISHED",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSampleQuarantine(ctx, "sha256:hidden", true, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := w.ReleaseSamples(ctx, "npm", "axios", "1.12.0", 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("a quarantined sample resolved at its readable URL: %+v (err=%v)", got, err)
	}
}
