package domain

import "testing"

// The record page picked the newest version with plain string comparison,
// so '7' > '1' and the most-read page on the site said npm/uuid was at
// 7.0.3 when the only evidence the network held was 14.0.1. Being wrong
// about a version number is being wrong about the one thing this site
// exists to be right about.
func TestCompareVersionsOrdersNumbersAsNumbers(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"14.0.1", "7.0.3", 1}, // the one that was backwards
		{"7.0.3", "14.0.1", -1},
		{"1.10.0", "1.9.0", 1},
		{"2.0.0", "10.0.0", -1},
		{"v1.21.0", "v1.9.0", 1},   // golang keeps its v prefix
		{"1.2", "1.2.1", -1},       // fewer segments sort below
		{"1.2.0", "1.2", 1},        //
		{"1.2.0", "1.2.0", 0},      //
		{"1.2.0", "v1.2.0", 0},     // the prefix is not a difference
		{"1.2.0-rc1", "1.2.0", -1}, // a pre-release precedes its release
		{"1.2.0", "1.2.0-rc1", 1},
		{"1.2.0-rc2", "1.2.0-rc10", -1},
		{"1.2.0+build9", "1.2.0", 0}, // build metadata does not rank
		{"1.10", "1.beta", 1},        // numeric outranks alphanumeric
		{"", "1.0.0", -1},
		{"", "", 0},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// Whatever it is given, it must be a strict weak ordering, or sorting with
// it is undefined behaviour rather than a wrong answer.
func TestCompareVersionsIsAntisymmetric(t *testing.T) {
	vs := []string{"", "1", "1.0", "1.0.0", "0.9.9", "14.0.1", "7.0.3",
		"v2", "2.0.0-alpha", "2.0.0", "2.0.0+meta", "1.2.3.4", "next", "1.x"}
	for _, a := range vs {
		for _, b := range vs {
			ab, ba := CompareVersions(a, b), CompareVersions(b, a)
			if ab != -ba {
				t.Errorf("CompareVersions(%q,%q)=%d but (%q,%q)=%d", a, b, ab, b, a, ba)
			}
		}
	}
}
