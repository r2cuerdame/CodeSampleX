//go:build windows

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

var attachConsoleTest = syscall.NewLazyDLL("kernel32.dll").NewProc("AttachConsole")

func TestPayloadDirectMCPAndDaemonStartOpenNoConsoleWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("builds payload binary")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "csx-payload.exe")
	buildCmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("compile payload binary: %v: %s", err, string(out))
	}
	report := filepath.Join(t.TempDir(), "report.txt")
	host := exec.Command(self, "-test.run=^TestPayloadConsolelessHostHelper$")
	host.Env = append(os.Environ(), "PAYLOAD_CONSOLELESS_HOST="+bin+"|"+report)
	host.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	if out, err := host.CombinedOutput(); err != nil {
		t.Fatalf("host failed: %v %s", err, out)
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	text := strings.TrimSpace(string(raw))
	if !strings.Contains(text, "hostConsole=false") {
		t.Fatalf("host still had console: %s", text)
	}
	if !strings.Contains(text, "mcpConsoleAttached=false") {
		t.Errorf("direct mcp payload still held a console window under consoleless host: %s", text)
	}
	if !strings.Contains(text, "mcpInitOK=true") || !strings.Contains(text, "mcpToolsOK=true") {
		t.Errorf("MCP stdio round-trip failed: %s", text)
	}
	if !strings.Contains(text, "daemonStartOK=true") || !strings.Contains(text, "daemonStatusOK=true") {
		t.Errorf("daemon start/status failed under consoleless host: %s", text)
	}
}

func TestPayloadConsolelessHostHelper(t *testing.T) {
	spec := os.Getenv("PAYLOAD_CONSOLELESS_HOST")
	if spec == "" {
		return
	}
	bin, report, ok := strings.Cut(spec, "|")
	if !ok {
		os.Exit(3)
	}
	home := filepath.Join(filepath.Dir(report), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		os.Exit(4)
	}
	hostCP, _ := windows.GetConsoleCP()

	cmd := exec.Command(bin, "mcp")
	cmd.Env = append(os.Environ(), "CSX_HOME="+home, "DO_NOT_TRACK=1")
	stdin, err1 := cmd.StdinPipe()
	stdout, err2 := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err1 != nil || err2 != nil || cmd.Start() != nil {
		_ = os.WriteFile(report, []byte("start failed"), 0o600)
		os.Exit(0)
	}
	reader := bufio.NewReader(stdout)
	_, _ = stdin.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"test\",\"version\":\"1\"}}}\n"))
	initLine, _ := reader.ReadString('\n')

	attached, _, _ := attachConsoleTest.Call(uintptr(cmd.Process.Pid))
	if attached != 0 {
		_, _, _ = freeConsole.Call()
	}

	_, _ = stdin.Write([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n"))
	_, _ = stdin.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n"))
	toolsLine, _ := reader.ReadString('\n')
	_ = stdin.Close()
	_ = cmd.Wait()

	startCmd := exec.Command(bin, "daemon", "start")
	startCmd.Env = append(os.Environ(), "CSX_HOME="+home, "DO_NOT_TRACK=1")
	startOut, startErr := startCmd.CombinedOutput()

	statusCmd := exec.Command(bin, "daemon", "status")
	statusCmd.Env = append(os.Environ(), "CSX_HOME="+home, "DO_NOT_TRACK=1")
	statusOut, statusErr := statusCmd.CombinedOutput()

	stopCmd := exec.Command(bin, "daemon", "stop")
	stopCmd.Env = append(os.Environ(), "CSX_HOME="+home, "DO_NOT_TRACK=1")
	_, _ = stopCmd.CombinedOutput()

	body := fmt.Sprintf(
		"hostConsole=%t mcpConsoleAttached=%t mcpInitOK=%t mcpToolsOK=%t daemonStartOK=%t daemonStatusOK=%t startOut=%q statusOut=%q",
		hostCP != 0,
		attached != 0,
		strings.Contains(initLine, `"serverInfo"`),
		strings.Contains(toolsLine, `"tools"`),
		startErr == nil && strings.Contains(string(startOut), "csx daemon running"),
		statusErr == nil && strings.Contains(string(statusOut), "csx daemon: running"),
		strings.TrimSpace(string(startOut)),
		strings.TrimSpace(string(statusOut)),
	)
	_ = os.WriteFile(report, []byte(body), 0o600)
	os.Exit(0)
}
