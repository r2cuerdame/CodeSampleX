package config

import "testing"

// LOCAL ONLY is one sentence long — "nothing about your projects ever leaves
// this machine" — and the README repeats it in nine languages as "local-only
// mode transmits nothing at all". A publicness probe hands a public registry
// the name of every dependency in the lockfile, so the only mode that may
// make one is the mode where the user agreed to take part.
func TestOnlyCommunityMayContactRegistries(t *testing.T) {
	if !MayContactRegistries(ModeCommunity) {
		t.Error("community mode cannot check publicness — nothing would ever be recorded")
	}
	for _, mode := range []string{ModeLocalOnly, ModeUninitialized, "something-else"} {
		if MayContactRegistries(mode) {
			t.Errorf("mode %q was allowed to name the user's packages to a public registry", mode)
		}
	}
}
