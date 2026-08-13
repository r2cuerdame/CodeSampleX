package cli

import "testing"

// TestMCPRegistered verifies the `csx mcp` command agent configs invoke is
// wired into the dispatcher (contract C8/C9).
func TestMCPRegistered(t *testing.T) {
	for _, c := range Commands() {
		if c.Name == "mcp" {
			if c.Run == nil {
				t.Fatalf("mcp command has no Run")
			}
			if c.Summary == "" {
				t.Errorf("mcp command has no summary")
			}
			return
		}
	}
	t.Fatalf("mcp command not registered")
}
