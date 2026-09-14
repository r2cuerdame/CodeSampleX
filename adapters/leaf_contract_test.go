package adapters

import (
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"testing"
)

func TestEveryDependencyObserverCanReportExplicitLeaves(t *testing.T) {
	for _, a := range All() {
		if _, ok := a.(scanner.EdgeScanner); !ok {
			continue
		}
		if _, ok := a.(scanner.NoDependencyScanner); !ok {
			t.Errorf("%s can observe edges but cannot complete a dependency-free coordinate", a.Ecosystem())
		}
	}
}
