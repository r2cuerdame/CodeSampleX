package domain

import "testing"

// Semver ignores build metadata for precedence, and a version carrying it
// is not a prerelease. A hyphen INSIDE the metadata looked like the
// prerelease separator, so 1.2.0+build-1 was read as prerelease "1" and
// sorted below plain 1.2.0.
//
// Version order decides which release is "V-1", so a mis-ordered pair
// points the regression rule at the wrong comparison — and that rule is
// the basis of every "broken here, fixed there" claim the network makes.
func TestBuildMetadataIsNotAPrerelease(t *testing.T) {
	if got := preRelease("1.2.0+build-1"); got != "" {
		t.Errorf("preRelease(\"1.2.0+build-1\") = %q, want empty", got)
	}
	if got := preRelease("1.2.0-rc.1+build-9"); got != "rc.1" {
		t.Errorf("a real prerelease was lost: %q", got)
	}
	if CompareVersions("1.2.0+build-1", "1.2.0") != 0 {
		t.Error("build metadata changed precedence; semver says it must not")
	}
	if CompareVersions("1.2.0-rc.1", "1.2.0") >= 0 {
		t.Error("a prerelease no longer sorts below its release")
	}
}
