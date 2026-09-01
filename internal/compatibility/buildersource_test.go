package compatibility

import (
	"os"
	"testing"
)

// readBuilderSource returns builder.go, for the assertions that are about the
// SHAPE of a pass rather than its output. A function that gathers the whole
// corpus cannot be bounded by anything, however correct its result, and no
// output check can see the difference.
func readBuilderSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("builder.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
