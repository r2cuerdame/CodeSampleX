package httpapi

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// A fixed public target is public. No registry has to be asked, and none can
// answer.
//
// The ingest gate resolved publicness by asking the registry checker, which
// exists to query npm, PyPI, crates.io and the rest. engine/unreal lives on
// none of them, so the answer was UNKNOWN, and UNKNOWN is refused -- which
// means every observation an Unreal project could ever produce was rejected
// on arrival, retried on the next sync, and rejected again forever.
//
// domain.IsWantedTarget is already the publicness boundary for exactly these
// coordinates; its own doc comment says so. It simply was not consulted here.
func TestAFixedPublicTargetNeedsNoRegistry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		purl   string
		public bool
	}{
		{"the unreal engine", "pkg:generic/engine%2Funreal@5.5", true},
		{"unity", "pkg:generic/engine%2Funity@6000.0", true},
		// Not every generic purl: the vocabulary is fixed, and an arbitrary
		// generic name is exactly what an attacker would send to get an
		// unchecked coordinate onto public pages.
		{"an arbitrary generic name", "pkg:generic/something%2Fmade-up@1.0.0", false},
		// And a registry package is still the registry's decision.
		{"an npm package", "pkg:npm/axios@1.12.0", false},
	} {
		p, err := domain.ParsePURL(tc.purl)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := publicTargetPublicness(p) == scanner.PublicnessPublic
		if got != tc.public {
			t.Errorf("%s (%s): shortcut says public=%v, want %v", tc.name, tc.purl, got, tc.public)
		}
	}
}

// A target without a concrete version is not one. The boundary requires it,
// because a coordinate with no version is not something anyone can reproduce.
func TestAPublicTargetWithoutAVersionIsNotPublic(t *testing.T) {
	p := domain.PURL{Ecosystem: "generic", Name: "engine/unreal"}
	if publicTargetPublicness(p) == scanner.PublicnessPublic {
		t.Error("a versionless engine coordinate was treated as public")
	}
}
