package web

import "testing"

// A dropdown whose only choice is "all" cannot filter anything. On a narrow
// slice most of them are that: hasown carries one environment, so OS, arch,
// package manager, execution context and libc each offered exactly one value
// and sat there as furniture — six controls that do nothing, hiding the two
// that do.
func TestAFilterWithOneValueIsNotOffered(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"version": "1.0.0", "os": "linux", "libc": "glibc"}},
		{Dims: map[string]string{"version": "2.0.0", "os": "linux", "libc": "glibc"}},
	}
	if !cubeFilterWorthOffering(cubeDimValues(facts, "version"), "") {
		t.Error("version varies across the slice and was dropped")
	}
	for _, dim := range []string{"os", "libc"} {
		if cubeFilterWorthOffering(cubeDimValues(facts, dim), "") {
			t.Errorf("%s has one value and was still offered", dim)
		}
	}
}

// A dimension the reader has already pinned stays, or they cannot unpin it.
func TestAPinnedFilterIsAlwaysOffered(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"version": "1.0.0", "os": "linux"}},
	}
	if !cubeFilterWorthOffering(cubeDimValues(facts, "os"), "linux") {
		t.Error("a pinned filter vanished, so it cannot be cleared")
	}
}
