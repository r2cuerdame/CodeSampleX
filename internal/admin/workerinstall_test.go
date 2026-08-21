package admin

import (
	"strings"
	"testing"
)

// In POSIX sh, `VAR=1 curl ... | sh` binds the variable to curl alone — the
// sh running install.sh never sees it, so install.sh's worker-only branch is
// false and the paste runs the full interactive `csx init`, which is exactly
// what the mode exists to avoid. The variable belongs on the consumer:
// `curl ... | CSX_WORKER_ONLY=1 sh`, the form llms-install.md documents.
func TestWorkerSetupCommandSetsTheVariableForTheShell(t *testing.T) {
	cmd := WorkerUnixCMD("https://codesamplex.dev")
	if !strings.Contains(cmd, "| CSX_WORKER_ONLY=1 sh") {
		t.Errorf("cmd = %q; CSX_WORKER_ONLY must be in the environment of the sh that runs install.sh", cmd)
	}
	if strings.HasPrefix(cmd, "CSX_WORKER_ONLY=1 curl") {
		t.Errorf("cmd = %q; the variable binds to curl there, which ignores it", cmd)
	}
}
