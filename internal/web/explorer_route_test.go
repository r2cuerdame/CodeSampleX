package web

import "testing"

// Every Go module at v2 or above was unreachable: the "/v5" in
// github.com/golang-jwt/jwt/v5 is part of the import path, and reading it as
// a version made the package page 404 under both spellings — with the suffix
// because the version does not exist, without it because the module really
// is named with it. Modules that have no suffix, like shopspring/decimal,
// worked, which is why it went unnoticed.
func TestSplitPackageRestKeepsGoMajorSuffixInTheName(t *testing.T) {
	cases := []struct {
		eco, rest             string
		name, version, symbol string
	}{
		{"golang", "github.com/golang-jwt/jwt/v5", "github.com/golang-jwt/jwt/v5", "", ""},
		{"golang", "github.com/go-chi/chi/v5", "github.com/go-chi/chi/v5", "", ""},
		{"golang", "github.com/go-chi/chi/v5/v5.3.1", "github.com/go-chi/chi/v5", "v5.3.1", ""},
		{"golang", "github.com/go-chi/chi/v5/v5.3.1/Router", "github.com/go-chi/chi/v5", "v5.3.1", "Router"},
		{"golang", "github.com/shopspring/decimal", "github.com/shopspring/decimal", "", ""},
		{"golang", "github.com/shopspring/decimal/v1.4.0", "github.com/shopspring/decimal", "v1.4.0", ""},
		{"golang", "gopkg.in/yaml.v3", "gopkg.in/yaml.v3", "", ""},
		// The exception is golang's alone: elsewhere a bare vN is a version.
		{"npm", "zod/v4", "zod", "v4", ""},
		{"cargo", "serde/1.0.229", "serde", "1.0.229", ""},
		{"npm", "@scope/pkg/1.2.3", "@scope/pkg", "1.2.3", ""},
		{"maven", "com.fasterxml.jackson.core/jackson-databind", "com.fasterxml.jackson.core/jackson-databind", "", ""},
		{"maven", "com.fasterxml.jackson.core/jackson-databind/2.21.4", "com.fasterxml.jackson.core/jackson-databind", "2.21.4", ""},
		{"maven", "org.example/2.0", "org.example/2.0", "", ""},
		{"maven", "org.example/2.0/1.4.0", "org.example/2.0", "1.4.0", ""},
	}
	for _, c := range cases {
		name, version, tail, ok := splitPackageRest(c.eco, c.rest)
		if !ok {
			t.Errorf("%s/%s: not routable", c.eco, c.rest)
			continue
		}
		symbol := ""
		if len(tail) == 1 {
			symbol = tail[0]
		}
		if name != c.name || version != c.version || symbol != c.symbol || len(tail) > 1 {
			t.Errorf("%s/%s -> (%q, %q, %v), want (%q, %q, %q)",
				c.eco, c.rest, name, version, tail, c.name, c.version, c.symbol)
		}
	}
}
