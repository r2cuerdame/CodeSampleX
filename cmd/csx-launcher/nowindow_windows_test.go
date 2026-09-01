//go:build windows

package main

import (
	"os"
	"os/exec"
	"testing"
)

// A launcher whose stdio is piped must not let the payload keep a console.
//
// Measured on this workstation while four MCP clients were running csx:
//
//	conhost 28656 hosts csx.exe 32896   (parent claude.exe)
//	conhost 25852 hosts csx.exe  5252   (parent claude.exe)
//	conhost 38788 hosts csx.exe 32800   (parent claude.exe)
//	conhost 16644 hosts csx.exe 49156   (parent claude.exe)
//
// and no conhost at all for the csx processes codex.exe had spawned. One MCP
// host allocates a console for the child and the other does not, so
// hasConsole() answered true for half of them and the payload inherited a
// console nobody reads — the window users report, titled with the payload
// path.
//
// Owning a console was the wrong question. What separates a person typing
// `csx search` from an MCP host is a pipe on both ends of stdio: a terminal
// gives console handles, a redirect to a file gives a file, the null device
// gives a character device, and only a host speaking a protocol over stdio
// gives pipes. Asking merely whether stdout was a console took the console
// away from `csx search > out.txt` as well, which still wants Ctrl+C.
func TestPipedStdioHidesTheChildEvenWithAConsole(t *testing.T) {
	if consoleIsTheOutput() {
		// `go test` from a terminal: the child must keep the console so its
		// output, prompts and Ctrl+C still work.
		cmd := exec.Command("cmd", "/c", "echo hi")
		applyWindowPolicy(cmd)
		if cmd.SysProcAttr != nil && cmd.SysProcAttr.CreationFlags&createNoWindow != 0 {
			t.Error("a terminal session had its child's console taken away")
		}
		return
	}
	cmd := exec.Command("cmd", "/c", "echo hi")
	applyWindowPolicy(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Error("piped stdio still let the payload open a window")
	}
}

// The decision reads the KIND of handle, not whether a console exists.
func TestTheWindowPolicyDistinguishesPipesFromEverythingElse(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if handleIsConsole(w.Fd()) {
		t.Error("a pipe was reported as a console")
	}
	if !handleIsPipe(w.Fd()) {
		t.Error("a pipe was not recognised as one")
	}

	// The null device is a character device, not a pipe: redirecting to it is
	// not an MCP host, and a session doing that keeps its console.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if handleIsPipe(null.Fd()) {
		t.Error("the null device was mistaken for a pipe")
	}

	// So is a plain file.
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if handleIsPipe(f.Fd()) {
		t.Error("a file was mistaken for a pipe")
	}
}
