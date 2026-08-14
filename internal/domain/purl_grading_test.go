package domain

import "testing"

// Semver makes a 0.x minor bump exactly as breaking as a major one, so
// grading with Major() reported axum 0.6 against 0.8 as a minor difference.
// Pre-1.0 is where most of Rust and a lot of Dart lives.
//
// Major() itself is deliberately unchanged: it generates shard keys, and
// moving it would invalidate every shard in the network.
func TestBreakingBucketSeparatesPre1Minors(t *testing.T) {
	cases := []struct{ purl, breaking, major string }{
		{"pkg:cargo/axum@0.6.20", "0.6", "0"},
		{"pkg:cargo/axum@0.8.1", "0.8", "0"},
		{"pkg:cargo/serde@1.0.229", "1", "1"},
		{"pkg:cargo/serde@1.2.0", "1", "1"},
		{"pkg:pub/intl@0.20.2", "0.20", "0"},
		{"pkg:golang/github.com/x/y@v1.4.0", "v1", "v1"},
	}
	for _, c := range cases {
		p, err := ParsePURL(c.purl)
		if err != nil {
			t.Fatalf("%s: %v", c.purl, err)
		}
		if got := p.BreakingBucket(); got != c.breaking {
			t.Errorf("%s BreakingBucket = %q, want %q", c.purl, got, c.breaking)
		}
		if got := p.Major(); got != c.major {
			t.Errorf("%s Major = %q, want %q (shard keys must not move)", c.purl, got, c.major)
		}
	}

	a, _ := ParsePURL("pkg:cargo/axum@0.6.20")
	b, _ := ParsePURL("pkg:cargo/axum@0.8.1")
	if a.Major() != b.Major() {
		t.Fatal("the test is not reproducing the old input: Major used to equate these")
	}
	if a.BreakingBucket() == b.BreakingBucket() {
		t.Error("0.6 and 0.8 still grade as the same line")
	}
}
