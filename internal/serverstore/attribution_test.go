package serverstore

import "testing"

func targetSet(rows []SnapshotTarget) map[SnapshotTarget]bool {
	out := map[SnapshotTarget]bool{}
	for _, r := range rows {
		out[r] = true
	}
	return out
}

// A sample's receipt resolves EVERY package in the lockfile, and the targets
// were the cartesian product of those against every symbol the sample
// declared. A Sinatra sample tested with minitest therefore filed
// Sinatra::Base under minitest, rack, rack-test, mustermann and
// rack-protection as well — and the front page, asked for minitest, answered
// with Faraday's API. In production 680 of 3,881 symbols were claimed by more
// than one package and one was claimed by 21.
//
// A symbol belongs to one package. When several claim it, the narrowest claim
// wins: the sample that resolved the fewest packages to demonstrate it is the
// one that says most about where it lives.
func TestSymbolGoesToTheNarrowestClaim(t *testing.T) {
	got := targetSet(snapshotTargetsFromClaims([]receiptClaim{
		{ // the subject sample: faraday alone
			Packages: []string{"pkg:gem/faraday@2.9.0"},
			Symbols:  []string{"Faraday::Adapter::NetHttp"},
		},
		{ // a suite that merely used minitest to exercise it
			Packages: []string{"pkg:gem/faraday@2.9.0", "pkg:gem/minitest@5.27.0", "pkg:gem/rack@3.2.7"},
			Symbols:  []string{"Faraday::Adapter::NetHttp"},
		},
	}))

	if !got[SnapshotTarget{PURL: "pkg:gem/faraday@2.9.0", Symbol: "Faraday::Adapter::NetHttp"}] {
		t.Error("faraday lost the symbol it owns")
	}
	if got[SnapshotTarget{PURL: "pkg:gem/minitest@5.27.0", Symbol: "Faraday::Adapter::NetHttp"}] {
		t.Error("minitest still claims Faraday's API")
	}
}

// Package-level targets are not symbol claims: every package a receipt
// resolved really was present, and the package-level snapshot is what carries
// that. Only the SYMBOL rows are narrowed.
func TestPackageLevelTargetsSurviveForEveryResolvedPackage(t *testing.T) {
	got := targetSet(snapshotTargetsFromClaims([]receiptClaim{{
		Packages: []string{"pkg:gem/faraday@2.9.0", "pkg:gem/minitest@5.27.0"},
		Symbols:  []string{"Faraday::Adapter::NetHttp"},
	}}))
	for _, purl := range []string{"pkg:gem/faraday@2.9.0", "pkg:gem/minitest@5.27.0"} {
		if !got[SnapshotTarget{PURL: purl, Symbol: ""}] {
			t.Errorf("%s lost its package-level target", purl)
		}
	}
}

// A symbol only ever seen in one wide sample has no narrower claim to lose
// to. Dropping it everywhere would delete coverage rather than correct it, so
// every package in that sample keeps it — exactly today's behaviour, which is
// the floor this change must not go below.
func TestASymbolWithNoNarrowerClaimKeepsEveryPackage(t *testing.T) {
	got := targetSet(snapshotTargetsFromClaims([]receiptClaim{{
		Packages: []string{"pkg:npm/a@1.0.0", "pkg:npm/b@1.0.0"},
		Symbols:  []string{"only.here"},
	}}))
	for _, purl := range []string{"pkg:npm/a@1.0.0", "pkg:npm/b@1.0.0"} {
		if !got[SnapshotTarget{PURL: purl, Symbol: "only.here"}] {
			t.Errorf("%s lost a symbol nothing else claims", purl)
		}
	}
}

// A stated subject is the narrowest claim there is: the authoring queue
// assigned that exact coordinate, so nothing has to be inferred from how many
// packages happened to resolve.
func TestAStatedSubjectWinsOutright(t *testing.T) {
	got := targetSet(snapshotTargetsFromClaims([]receiptClaim{{
		Packages: []string{"pkg:gem/faraday@2.9.0", "pkg:gem/minitest@5.27.0"},
		Symbols:  []string{"Faraday::Adapter::NetHttp"},
		Subject:  "pkg:gem/faraday@2.9.0",
	}}))
	if !got[SnapshotTarget{PURL: "pkg:gem/faraday@2.9.0", Symbol: "Faraday::Adapter::NetHttp"}] {
		t.Error("the stated subject did not receive its own symbol")
	}
	if got[SnapshotTarget{PURL: "pkg:gem/minitest@5.27.0", Symbol: "Faraday::Adapter::NetHttp"}] {
		t.Error("a package the sample merely resolved kept the subject's symbol")
	}
}
