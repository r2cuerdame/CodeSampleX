// Temporary fixture for R2C-57. The red version of this file is what the `main`
// ruleset refused to merge; this green version is the same pull request with the
// only variable changed, so a `CLEAN` reading afterwards means the check, and
// not something else about the branch, is what the gate was reading.
package sanitizer

import "testing"

func TestMergeGateFixtureIsNowGreen(t *testing.T) {
	t.Log("R2C-57 merge gate fixture: this run is deliberately green")
}
