package domain

import "testing"

// Every ecosystem this network verifies samples in has to be one it can
// record a run in. A contract run is an execution on a real machine, and
// refusing to record it leaves the coordinate reading "never measured" about
// work we did ourselves.
//
// Measured after the receipt backfill: 8,467 runs were recorded and 1,467
// refused, and every refusal was gem, hex or pub. The 938 snapshot rows left
// reading "never measured" were exactly those three ecosystems and nothing
// else.
//
// They sit where maven already sits. The client scanner ships adapters for
// npm, pypi, golang and cargo only; maven is on this list because a
// verification-only ecosystem may publish signed sample evidence without
// scanning anybody's local project, which is the same standing gem, hex and
// pub have.
func TestEveryVerifiedEcosystemCanRecordARun(t *testing.T) {
	for _, ecosystem := range []string{"gem", "hex", "pub"} {
		if !AllowedEcosystems[ecosystem] {
			t.Errorf("%s samples are verified but a run in one cannot be recorded", ecosystem)
		}
	}
}
