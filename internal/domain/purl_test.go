package domain

import "testing"

func TestParsePURL(t *testing.T) {
	cases := []struct {
		in                  string
		eco, name, version  string
		major, majorMinor   string
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
