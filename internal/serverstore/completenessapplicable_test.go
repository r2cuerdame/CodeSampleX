package serverstore

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The backlog counted work nobody could ever do.
//
// Measured on production: 398 npm per-platform native builds and one Gradle
// plugin marker sat inside the 1,372 releases reported as having no sample,
// and the authoring queue declined every one of them on every poll — the
// queue's judgement and the census denominator disagreed by 399 coordinates.
// 507 more were reported as having no dependency graph in ecosystems where no
// scanner ships, so nothing could produce one.
//
// #87 wants a scheduler driven by this census. Built on that denominator it
// would hand out work nobody can close, which is the failure it was opened
// for.
func TestTheCensusDoesNotCountWorkNobodyCanDo(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	// One ordinary coordinate, one npm per-platform native build, one Gradle
	// plugin marker, one gem release and one pub release; the last three are
	// in ecosystems with a verifier image and no local scanner at all.
	for _, pkg := range []PackageRow{
		{PURL: "pkg:npm/axios@1.12.0", Ecosystem: "npm", Name: "axios", Version: "1.12.0", Publicness: "PUBLIC"},
		{PURL: "pkg:npm/%40esbuild/win32-x64@0.28.1", Ecosystem: "npm", Name: "@esbuild/win32-x64", Version: "0.28.1", Publicness: "PUBLIC"},
		{PURL: "pkg:maven/org.jetbrains.kotlin.plugin.serialization.gradle.plugin@2.2.20", Ecosystem: "maven", Name: "org.jetbrains.kotlin.plugin.serialization.gradle.plugin", Version: "2.2.20", Publicness: "PUBLIC"},
		{PURL: "pkg:gem/nokogiri@1.18.10", Ecosystem: "gem", Name: "nokogiri", Version: "1.18.10", Publicness: "PUBLIC"},
		{PURL: "pkg:pub/shared_preferences@2.5.3", Ecosystem: "pub", Name: "shared_preferences", Version: "2.5.3", Publicness: "PUBLIC"},
	} {
		if err := f.UpsertPackage(ctx, pkg); err != nil {
			t.Fatal(err)
		}
	}

	got, err := f.FarmCompletenessNow(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if got.SampleNotApplicable != 2 {
		t.Errorf("SampleNotApplicable = %d, want 2 (the platform build and the plugin marker)", got.SampleNotApplicable)
	}
	// npm and maven differ here: npm has a dependency scanner, maven does not.
	if got.DependencyNotApplicable != 3 {
		t.Errorf("DependencyNotApplicable = %d, want 3 (the maven marker, the gem and the pub release)", got.DependencyNotApplicable)
	}
	// Evidence is what `csx run` records, and it records nothing in an
	// ecosystem with no local project scanner: the Farm handed pub EVIDENCE
	// work out anyway and every lease came back empty (#387).
	if got.EvidenceNotApplicable != 3 {
		t.Errorf("EvidenceNotApplicable = %d, want 3 (the maven marker, the gem and the pub release)", got.EvidenceNotApplicable)
	}

	// The marker is unaskable on every axis and leaves the backlog. The gem
	// and pub releases are unaskable on two axes and stay: a sample can still
	// be written for them, and an N/A axis justifies only its own absence.
	total := 0
	for _, n := range got.States {
		total += n
	}
	if total != 4 {
		t.Errorf("States holds %d coordinates, want 4: the marker has no askable axis left, the gem and pub releases still need a Sample", total)
	}

	// And an ecosystem nobody can scan must not be reported as a dependency
	// gap somebody could close. axios and the esbuild platform build both
	// count: the platform build can never hold a SAMPLE, but its tree is an
	// npm lockfile like any other, and the two axes are separate assets.
	if got.DependencyUnknown != 2 {
		t.Errorf("DependencyUnknown = %d, want 2 — the two npm releases, whose trees are readable and unread", got.DependencyUnknown)
	}
}

// The queue and the census must reach the same verdict, because they used to
// reach different ones and the difference was the backlog.
func TestTheQueueAndTheCensusAgreeOnWhatCannotBeSampled(t *testing.T) {
	for _, tc := range []struct {
		ecosystem, name string
		want            bool
	}{
		{"npm", "@esbuild/win32-x64", true},
		{"npm", "@tailwindcss/oxide-darwin-arm64", true},
		{"npm", "@tailwindcss/oxide", false},
		{"npm", "axios", false},
		{"npm", "node-linux-utils", false},
		{"maven", "org.jetbrains.kotlin.plugin.serialization.gradle.plugin", true},
		{"maven", "com.google.guava/guava", false},
		{"gem", "nokogiri", false},
	} {
		_, got := domain.SampleNotApplicable(tc.ecosystem, tc.name)
		if got != tc.want {
			t.Errorf("SampleNotApplicable(%s, %s) = %v, want %v", tc.ecosystem, tc.name, got, tc.want)
		}
	}
}

// The dependency rule is a fact about the scanner, not about the package. A
// gem has dependencies; saying otherwise would be the network
// asserting something it never measured.
func TestTheDependencyRuleSaysNobodyCanLookNotThatThereIsNothing(t *testing.T) {
	for _, eco := range []string{"npm", "pypi", "cargo"} {
		if _, na := domain.DependencyNotApplicable(eco); na {
			t.Errorf("%s ships a dependency scanner and must stay askable", eco)
		}
	}
	// golang is deliberately absent: goadapter ships an EdgeScanner, so a
	// Go release with no graph is unmeasured rather than unaskable.
	for _, eco := range []string{"maven", "gem", "hex", "pub", "composer"} {
		reason, na := domain.DependencyNotApplicable(eco)
		if !na {
			t.Errorf("%s has no dependency scanner and would be counted as a closable gap", eco)
		}
		if reason == "" {
			t.Errorf("%s closed with no reason; a coordinate must not leave the backlog silently", eco)
		}
	}
}

// The evidence rule is likewise a fact about the scanner. A pub package
// builds and runs like any other; what is missing is a local project scanner
// that could name it, so `csx run` in a Flutter project records nothing
// pub-shaped, and an EVIDENCE lease there could never be closed (#387).
func TestTheEvidenceRuleSaysNobodyCanObserveNotThatNothingRan(t *testing.T) {
	for _, eco := range []string{"npm", "pypi", "cargo", "golang"} {
		if _, na := domain.EvidenceNotApplicable(eco); na {
			t.Errorf("%s ships a project scanner and must stay askable", eco)
		}
	}
	for _, eco := range []string{"maven", "gem", "hex", "pub", "composer"} {
		reason, na := domain.EvidenceNotApplicable(eco)
		if !na {
			t.Errorf("%s has no project scanner and would be handed Evidence work nobody can close", eco)
		}
		if !strings.Contains(reason, eco) {
			t.Errorf("%s closed with reason %q; it must name the ecosystem whose lane is missing", eco, reason)
		}
		// The store-level rule the snapshot, the live re-check and the claim
		// gate read is the same sentence.
		if got, _ := AuthoringAxisNotApplicable(WantedRow{Ecosystem: eco, Axis: AuthoringAxisEvidence}); got != reason {
			t.Errorf("AuthoringAxisNotApplicable(%s, EVIDENCE) = %q, want the domain sentence %q", eco, got, reason)
		}
		if _, na := AuthoringAxisNotApplicable(WantedRow{Ecosystem: eco, Axis: AuthoringAxisSample}); na {
			t.Errorf("%s: the Sample axis must stay askable in a verifier-only ecosystem", eco)
		}
	}
}
