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
		fail(err)
	}
	root := filepath.Dir(self)
	a, err := launcher.Load(root)
	if err != nil {
		fail(err)
	}
	payload, err := launcher.PayloadPath(root, a.Current.Version)
	if err != nil {
		fail(err)
	}
	cmd := exec.Command(payload, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = launcherEnv(os.Environ(), self, root, a.Current, launcher.ProtocolVersion)
	code, err := runChild(cmd)
	if err != nil {
		fail(err)
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

func fail(err error) { fmt.Fprintln(os.Stderr, "csx launcher:", err); os.Exit(126) }
