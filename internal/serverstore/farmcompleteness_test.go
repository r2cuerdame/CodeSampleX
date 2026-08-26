package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// R2C-126. The panel could say how many coordinates were unproven and nothing
// about the other two assets, so a release with a sample and no resolved graph
// read as finished work. The production unit is not "has a sample": it is
// which of Sample, Evidence and Dependency this coordinate has.
//
// Measured against production on 2026-08-23 over 2,880 PUBLIC releases:
// 1,763 carried evidence alone, 881 a sample and evidence, 166 all three, 47
// evidence and a graph, 17 nothing at all and 6 a sample alone. Two thirds of
// the corpus was incomplete on an axis nothing counted.
func TestFarmCompletenessCountsTheEightStates(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	// One coordinate per state, named for the state it is in.
	//
	// S-D, S-- and --D are seeded and stay empty, which is a fact about the data
	// rather than a gap in the fixture: the only thing that records a
	// resolved graph today is an observation batch, and a batch carries the
	// package it is about -- so a coordinate with a graph necessarily has
	// evidence. Likewise, a passing receipt is itself evidence, so a sample
	// cannot occupy a no-evidence cell. The panel keeps all eight cells so the
	// day a resolution arrives from
	// somewhere else, the number moves instead of the shape changing.
	seedCompletenessCoordinate(t, f, "sed", true, true, true, now)
	seedCompletenessCoordinate(t, f, "se", true, true, false, now)
	seedCompletenessCoordinate(t, f, "sd", true, false, true, now)
	seedCompletenessCoordinate(t, f, "s", true, false, false, now)
	seedCompletenessCoordinate(t, f, "ed", false, true, true, now)
	seedCompletenessCoordinate(t, f, "e", false, true, false, now)
	seedCompletenessCoordinate(t, f, "d", false, false, true, now)
	seedCompletenessCoordinate(t, f, "none", false, false, false, now)

	got, err := f.FarmCompletenessNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"SED": 1, "SE-": 2, "S-D": 0, "S--": 0,
		"-ED": 1, "-E-": 1, "--D": 0, "---": 1,
	}
	// The two coordinates seeded with a graph and no evidence land in SED and
	// -ED, because recording the graph gave them evidence.
	want["SED"] = 2
	want["-ED"] = 2
	for state, n := range want {
		if got.States[state] != n {
			t.Errorf("state %s = %d, want %d (all: %v)", state, got.States[state], n, got.States)
		}
	}
	if len(got.States) != 8 {
		t.Errorf("states = %v, want all eight cells present", got.States)
	}
	total := 0
	for _, n := range got.States {
		total += n
	}
	if total != 8 {
		t.Errorf("the eight cells sum to %d, want 8 -- every coordinate is in exactly one", total)
	}
}

// A contract receipt is executed evidence, even when the network has not yet
// ingested an observation batch for the same release. Both stores must keep
// that coordinate out of the misleading Sample-without-Evidence cell.
func TestFarmCompletenessCountsPassingReceiptAsEvidence(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		store completenessStore
	}{
		{name: "fake", store: NewFake()},
		{name: "postgres", store: openTestPG(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedCompletenessCoordinate(t, tc.store, "receipt-only-"+tc.name, true, false, false, now)
			got, err := tc.store.(FarmCompletenessStore).FarmCompletenessNow(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.States["SE-"] != 1 {
				t.Fatalf("SE- = %d, want 1 for a passing receipt without an observation: %v", got.States["SE-"], got.States)
			}
			if got.States["S--"] != 0 {
				t.Fatalf("S-- = %d, want 0: a passing receipt is evidence", got.States["S--"])
			}
		})
	}
}

// The distinction the whole axis exists for. A coordinate nobody resolved and
// a coordinate resolved to nothing are not the same answer, and only one of
// them is a fact -- R2C-108 renders this, and printing "no dependencies" for
// silence would be the network asserting something it never measured.
func TestFarmCompletenessKeepsUnknownApartFromProvenNoDependencies(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	seedCompletenessCoordinate(t, f, "haslib", false, true, true, now)
	seedCompletenessCoordinate(t, f, "nevertried", false, true, false, now)

	got, err := f.FarmCompletenessNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.DependencyGraph != 1 {
		t.Errorf("dependency graph = %d, want 1", got.DependencyGraph)
	}
	if got.DependencyUnknown != 1 {
		t.Errorf("dependency unknown = %d, want 1", got.DependencyUnknown)
	}
	if got.DependencyProvenNone != 0 {
		t.Errorf("dependency proven-none = %d, want 0: nothing has measured one yet",
			got.DependencyProvenNone)
	}
	if total := got.DependencyGraph + got.DependencyUnknown + got.DependencyProvenNone; total != 2 {
		t.Errorf("the three dependency states sum to %d, want 2 -- every coordinate has exactly one", total)
	}
}

// seedCompletenessCoordinate writes one PUBLIC release in the requested state.
// A sample means a non-quarantined sample with a passing receipt; evidence
// means an observation; a dependency means a resolved lockfile named this
// release's children.
func seedCompletenessCoordinate(t *testing.T, store completenessStore, name string,
	sample, evidence, dependency bool, now time.Time) {
	t.Helper()
	ctx := context.Background()
	const version = "1.0.0"
	purl := "pkg:npm/" + name + "@" + version
	if evidence {
		seedObservedPackage(t, store, name, version, "windows", 5, true)
	} else if err := store.UpsertPackage(ctx, PackageRow{
		PURL: purl, Ecosystem: "npm", Name: name, Version: version,
		Major: "1", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
	if sample {
		id := "sha256:proof-" + name
		if err := store.SaveSample(ctx, SampleRow{
			SampleID:     id,
			ManifestJSON: `{"packages":["` + purl + `"],"symbols":[]}`,
			Status:       "CROSS_PASS", License: "MIT-0", CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveReceipt(ctx, ReceiptRow{
			SampleID: id, ReceiptID: "r-" + name, PeerID: "peer-" + name,
			EnvHash: "env-" + name, ContractResult: "PASS",
			ReceiptJSON: `{"environment":{"os":"linux"}}`, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if dependency {
		// A resolution that named this release's children: the release is the
		// PARENT of an edge, which is the only thing that says the network
		// knows what it pulls.
		if _, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{{
			SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "anon-dep-" + name,
			ProjectBucket: "proj-dep-" + name, Package: purl,
			Stage: domain.StageProjectCompile, Result: domain.ResultPass,
			ObservationCount: 1, Direct: true,
			DependsOn: []string{"pkg:npm/child-of-" + name + "@2.0.0"},
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64",
				Runtime: "node", RuntimeVersion: "22",
			},
		}}); err != nil || len(rejected) != 0 {
			t.Fatalf("ingest dependency for %s: rejected=%v err=%v", name, rejected, err)
		}
	}
}

// The two stores compute the matrix from two sets of predicates, and the
// repo's history is that halves of a shared definition drift until a panel
// prints a number the queue does not agree with.
func TestIntegrationFarmCompletenessMatchesPostgres(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	play := func(t *testing.T, store completenessStore) FarmCompleteness {
		t.Helper()
		seedCompletenessCoordinate(t, store, "sed", true, true, true, now)
		seedCompletenessCoordinate(t, store, "se", true, true, false, now)
		seedCompletenessCoordinate(t, store, "s", true, false, false, now)
		seedCompletenessCoordinate(t, store, "ed", false, true, true, now)
		seedCompletenessCoordinate(t, store, "e", false, true, false, now)
		seedCompletenessCoordinate(t, store, "none", false, false, false, now)
		got, err := store.(FarmCompletenessStore).FarmCompletenessNow(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	fake := NewFake()
	fake.NowFn = func() time.Time { return now }
	fakeGot := play(t, fake)
	pgGot := play(t, openTestPG(t))

	for _, state := range completenessStates {
		if fakeGot.States[state] != pgGot.States[state] {
			t.Errorf("state %s: fake=%d pg=%d\n fake: %v\n pg:   %v",
				state, fakeGot.States[state], pgGot.States[state], fakeGot.States, pgGot.States)
		}
	}
	if fakeGot.DependencyGraph != pgGot.DependencyGraph ||
		fakeGot.DependencyUnknown != pgGot.DependencyUnknown ||
		fakeGot.DependencyProvenNone != pgGot.DependencyProvenNone {
		t.Errorf("dependency split differs: fake=%+v pg=%+v", fakeGot, pgGot)
	}
}
