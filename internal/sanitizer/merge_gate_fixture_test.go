// Temporary fixture for R2C-57. It exists only to make the `Test` check red on
// one pull request, so that the `main` ruleset can be observed refusing the
// merge. The branch and this file are deleted once that is recorded.
package sanitizer

import "testing"

func TestMergeGateFixtureIsDeliberatelyRed(t *testing.T) {
	t.Fatal("R2C-57 merge gate fixture: this failure is deliberate")
}
