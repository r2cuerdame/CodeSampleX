//go:build windows

package evidence

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Windows stores process status as a DWORD. cmd's exit -1 therefore reaches
// os/exec as 4294967295 on 64-bit Go unless the producer establishes the
// signed public-wire boundary explicitly.
func TestRunCanonicalizesWindowsDWORDExitStatus(t *testing.T) {
	t.Setenv("CSX_HOME", t.TempDir())
	code, output, err := Run(context.Background(), []string{"cmd", "/c", "exit", "-1"}, t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != -1 {
		t.Fatalf("exit code = %d, want signed int32 -1", code)
	}
	if output.Termination.Kind != domain.TerminationExit || output.Termination.ExitCode == nil || *output.Termination.ExitCode != -1 {
		t.Fatalf("termination = %+v, want exit:-1", output.Termination)
	}
}
