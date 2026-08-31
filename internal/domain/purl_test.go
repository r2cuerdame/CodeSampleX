package domain

import "testing"

func TestParsePURL(t *testing.T) {
	cases := []struct {
		in                 string
		eco, name, version string
		major, majorMinor  string
	}{
		{"pkg:npm/axios@1.12.0", "npm", "axios", "1.12.0", "1", "1.12"},
		{"pkg:npm/%40scope/name@2.0.1", "npm", "@scope/name", "2.0.1", "2", "2.0"},
		{"pkg:npm/@scope/name@2.0.1", "npm", "@scope/name", "2.0.1", "2", "2.0"},
		{"pkg:pypi/fastapi@0.116.0", "pypi", "fastapi", "0.116.0", "0", "0.116"},
		{"pkg:cargo/serde@1.0.219", "cargo", "serde", "1.0.219", "1", "1.0"},
		{"pkg:golang/github.com/example/module@v1.2.0", "golang", "github.com/example/module", "v1.2.0", "v1", "v1.2"},
		{"pkg:npm/axios@1.13.0-beta.1", "npm", "axios", "1.13.0-beta.1", "1", "1.13"},
	}
	for _, c := range cases {
		p, err := ParsePURL(c.in)
		if err != nil {
			t.Fatalf("ParsePURL(%q): %v", c.in, err)
		}
		if p.Ecosystem != c.eco || p.Name != c.name || p.Version != c.version {
			t.Errorf("ParsePURL(%q) = %+v", c.in, p)
		}
		if p.Major() != c.major {
			t.Errorf("%q Major() = %q, want %q", c.in, p.Major(), c.major)
		}
		if p.MajorMinor() != c.majorMinor {
			t.Errorf("%q MajorMinor() = %q, want %q", c.in, p.MajorMinor(), c.majorMinor)
		}
	}
}

func TestConcreteResolvedVersionRejectsRequestsAndProtocols(t *testing.T) {
	for _, version := range []string{"1.2.3", "v0.0.0-20260817010101-abcdef123456", "1!2.0+linux", "2.0.0.pre"} {
		if !ConcreteResolvedVersion(version) {
			t.Errorf("%q should be a concrete resolved version", version)
		}
	}
	for _, version := range []string{"", "latest", "^1.2.3", ">=2", "~1.0", "file:../x", "https://example/x", "1.2 || 2"} {
		if ConcreteResolvedVersion(version) {
			t.Errorf("%q should not be accepted as a concrete resolved version", version)
		}
	}
}

func TestParsePURLRoundTrip(t *testing.T) {
	for _, in := range []string{
		"pkg:npm/axios@1.12.0",
		"pkg:npm/%40scope/name@2.0.1",
		"pkg:golang/github.com/example/module@v1.2.0",
	} {
		p, err := ParsePURL(in)
		if err != nil {
			t.Fatal(err)
		}
		p2, err := ParsePURL(p.String())
		if err != nil {
			t.Fatalf("round trip parse of %q: %v", p.String(), err)
		}
		if p2 != p {
			t.Errorf("round trip %q: %+v != %+v", in, p2, p)
		}
	}
}

func TestParsePURLErrors(t *testing.T) {
	for _, in := range []string{
		"", "axios@1.0.0", "pkg:", "pkg:npm/", "pkg:npm/axios",
		"pkg:/axios@1.0.0", "pkg:npm/axios@", "pkg:npm/@1.0.0",
	} {
		if _, err := ParsePURL(in); err == nil {
			t.Errorf("ParsePURL(%q): expected error", in)
		}
	}
}

// A Go module version is canonically v-prefixed, and a bare one is malformed
// rather than an alternative spelling.
//
// ParsePURL stored the version verbatim, so one release entered the corpus
// twice: `github.com/jackc/pgx/v5@v5.10.0` sat at rank 4 on the wanted board
// while `@5.10.0` was a separate row beneath it. Their asks did not add up, so
// the board ranked a real demand lower than it was and showed a reader two
// entries for one question. Production on 2026-08-31 held 8 such package rows
// across 6 names and 16 wanted rows.
//
// It splits more than the board. dependency_edge, version_coresidence and the
// package and version pages all key on the version string, so the same module
// is two coordinates in the dependency map — which is exactly the map golang
// had only just started contributing to.
func TestABareGoModuleVersionIsNormalised(t *testing.T) {
	p, err := ParsePURL("pkg:golang/github.com/jackc/pgx/v5@5.10.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "v5.10.0" {
		t.Errorf("version = %q, want %q", p.Version, "v5.10.0")
	}
	if got := p.String(); got != "pkg:golang/github.com/jackc/pgx/v5@v5.10.0" {
		t.Errorf("String() = %q", got)
	}
}

// The canonical spelling is left exactly as it is — including the shapes the
// Go tool emits that are not plain semver.
func TestCanonicalGoVersionsAreUntouched(t *testing.T) {
	for _, want := range []string{
		"v5.10.0",
		"v0.0.0-20240606120523-5a60cdf6a761", // pseudo-version
		"v2.0.0+incompatible",
	} {
		p, err := ParsePURL("pkg:golang/example.com/mod@" + want)
		if err != nil {
			t.Fatal(err)
		}
		if p.Version != want {
			t.Errorf("version = %q, want %q", p.Version, want)
		}
	}
}

// Only golang, and only something that looks like a version.
//
// Every other ecosystem writes bare versions canonically, so prefixing there
// would invent the split this change exists to remove. And a version that does
// not begin with a digit is not a Go release with a missing prefix — turning
// "latest" into "vlatest" would fabricate a coordinate rather than repair one.
func TestNormalisationIsNarrow(t *testing.T) {
	for _, tc := range []struct{ purl, want string }{
		{"pkg:npm/axios@1.12.0", "1.12.0"},
		{"pkg:pypi/llvmlite@0.44.0", "0.44.0"},
		{"pkg:cargo/serde@1.0.0", "1.0.0"},
		{"pkg:golang/example.com/mod@latest", "latest"},
	} {
		p, err := ParsePURL(tc.purl)
		if err != nil {
			t.Fatal(err)
		}
		if p.Version != tc.want {
			t.Errorf("%s -> version %q, want %q", tc.purl, p.Version, tc.want)
		}
	}
}
