package search

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Go, Python, Elixir and Dart keep their entire release history in the
// SECOND version segment: go1.9 and go1.26 are seven years apart and both
// "1"; python 3.6 and 3.12 are both "3". The client grader compared majors
// only, so it called them the same runtime version — and printed "go 1" in
// the list of things that MATCH.
//
// The server-side grader was fixed for this and the client was not, so one
// request got two different answers: EXACT here, ADAPTATION_REQUIRED there.
// Whoever was running the MCP got the wrong one.
func TestGoAndPythonMinorsAreDifferentReleaseLines(t *testing.T) {
	cases := []struct {
		runtime, a, b string
		same          bool
	}{
		{"go", "1.26.0", "1.9.7", false},
		{"go", "1.26.0", "1.26.3", true},
		{"python", "3.12.4", "3.6.15", false},
		{"python", "3.12.4", "3.12.9", true},
		{"elixir", "1.18.0", "1.14.0", false},
		{"dart", "3.5.0", "2.19.0", false},
		// Everything else still compares by major: node 22.1 and 22.14 are
		// the same line, node 22 and 18 are not.
		{"node", "22.1.0", "22.14.0", true},
		{"node", "22.1.0", "18.20.0", false},
		{"bun", "1.1.0", "1.2.0", true},
	}
	for _, c := range cases {
		got := releaseLineOf(c.runtime, c.a) == releaseLineOf(c.runtime, c.b)
		if got != c.same {
			t.Errorf("%s %s vs %s: sameLine=%v, want %v", c.runtime, c.a, c.b, got, c.same)
		}
	}
}

// The grade itself must move with it: a seven-year runtime gap cannot be
// reported as EXACT with an empty difference list.
func TestARuntimeGenerationGapIsNotAnExactMatch(t *testing.T) {
	req := domain.EnvironmentFingerprint{
		SchemaVersion: 1, OS: "linux", Arch: "amd64",
		Runtime: "go", RuntimeVersion: "1.26.0", Language: "go", Ecosystem: "golang",
	}
	sam := req
	sam.RuntimeVersion = "1.9.7"

	dims := compareEnv(req, sam, "golang", false)
	for _, d := range dims {
		if d.equal && d.exactEntry == "go 1.9" {
			t.Error("go 1.9 was reported as matching go 1.26")
		}
	}
	var differed bool
	for _, d := range dims {
		if !d.equal {
			differed = true
		}
	}
	if !differed {
		t.Error("no dimension reported a difference between go 1.26 and go 1.9")
	}
}
