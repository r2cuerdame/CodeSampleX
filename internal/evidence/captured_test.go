package evidence

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelperCapturedStdin(t *testing.T) {
	if os.Getenv("CSX_TEST_CAPTURED_STDIN") != "1" {
		return
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	fmt.Print(len(data))
	os.Exit(0)
}
func TestCapturedCommandCannotConsumeMCPRequestInput(t *testing.T) {
	file := filepath.Join(t.TempDir(), "protocol-input")
	if err := os.WriteFile(file, []byte("{\"jsonrpc\":\"2.0\",\"id\":999}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = original; input.Close() }()
	t.Setenv("CSX_TEST_CAPTURED_STDIN", "1")
	code, output, err := RunCaptured(context.Background(), []string{os.Args[0], "-test.run=^TestHelperCapturedStdin$"}, t.TempDir())
	if err != nil || code != 0 || strings.TrimSpace(output.Stdout) != "0" {
		t.Fatalf("captured child received protocol input: %d %q %v", code, output.Stdout, err)
	}
	position, err := input.Seek(0, io.SeekCurrent)
	if err != nil || position != 0 {
		t.Fatalf("protocol input was consumed: %d %v", position, err)
	}
}
