package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--launcher-version" {
		fmt.Println("csx-launcher " + launcher.ProtocolVersion)
		return
	}
	self, err := os.Executable()
	if err != nil {
		fail(launcher.ReasonPointerUnreadable, err)
	}
	root := filepath.Dir(self)
	res, err := launcher.Resolve(root)
	if err != nil {
		fail(launcher.Reason(err), err)
	}
	if res.Recovered {
		// Diagnostics go to stderr and nowhere else. An MCP host reads this
		// process's stdout as JSON-RPC framing, so a recovery note written
		// there would corrupt the very session the recovery exists to save.
		fmt.Fprintf(os.Stderr, "csx launcher: recovered: %s: current payload %s is unusable; running last-known-good %s\n",
			res.FailedReason, res.FailedVersion, res.Descriptor.Version)
		if !res.Healed {
			fmt.Fprintf(os.Stderr, "csx launcher: active pointer was not repaired: %v\n", res.HealError)
		}
	}
	cmd := exec.Command(res.PayloadPath, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = launcherEnv(os.Environ(), self, root, res.Descriptor, launcher.ProtocolVersion)
	code, err := runChild(cmd)
	if err != nil {
		fail(launcher.ReasonPayloadStartFailed, err)
	}
	// ExitCode reports -1 for a process that was signalled or never exited.
	// Passing that to os.Exit launders a failure into a platform-dependent
	// code, so it becomes the launcher's own stable one.
	if code < 0 {
		fail(launcher.ReasonPayloadStartFailed, fmt.Errorf("payload did not report an exit status (%d)", code))
	}
	if code != 0 {
		os.Exit(code)
	}
}

func launcherEnv(env []string, launcherPath, root string, d launcher.Descriptor, protocol string) []string {
	out := make([]string, 0, len(env)+6)
	for _, item := range env {
		upper := strings.ToUpper(item)
		if strings.HasPrefix(upper, "CSX_LAUNCHER_") || strings.HasPrefix(upper, "CSX_ACTIVE_") || strings.HasPrefix(upper, "CSX_PAYLOAD_VERSION=") {
			continue
		}
		out = append(out, item)
	}
	return append(out, "CSX_LAUNCHER_PATH="+launcherPath, "CSX_LAUNCHER_ROOT="+root, "CSX_LAUNCHER_VERSION="+protocol,
		"CSX_PAYLOAD_VERSION="+d.Version, "CSX_ACTIVE_SEQUENCE="+strconv.FormatUint(d.Sequence, 10), "CSX_ACTIVE_SHA256="+d.SHA256)
}

// fail is the only way this launcher stops without having run a payload, and it
// always stops non-zero. A caller that cannot execute csx must never be able to
// read that as the command having succeeded -- on MCP stdio the whole failure
// is otherwise an empty stdout and a clean exit, which a host reports as a
// server that closed rather than one that could not start. The reason code
// leads the line so that message stays greppable across platforms.
func fail(reason string, err error) {
	fmt.Fprintf(os.Stderr, "csx launcher: %s: %v\n", reason, err)
	os.Exit(126)
}
