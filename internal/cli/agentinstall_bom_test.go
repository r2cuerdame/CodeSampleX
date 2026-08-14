package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Windows editors write a UTF-8 BOM routinely and encoding/json refuses it,
// so a user who had ever opened their agent config in Notepad got "parse
// failed, left untouched" and no MCP registration at all. The install
// reported success and the agent simply never saw csx.
func TestAgentConfigWithABOMStillGetsRegistered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"mcpServers":{"other":{"command":"x"}}}`)...)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := mergeJSONFile(path, func(m map[string]any) {
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
			m["mcpServers"] = servers
		}
		servers["csx"] = map[string]any{"command": "csx", "args": []any{"mcp"}}
	})
	if err != nil {
		t.Fatalf("a BOM must not stop registration: %v", err)
	}
	if !changed {
		t.Fatal("nothing was written")
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"csx"`)) {
		t.Error("csx was not registered")
	}
	if !bytes.Contains(out, []byte(`"other"`)) {
		t.Error("the existing entry was lost")
	}
	// The BOM was theirs, not ours; dropping it is a change to someone
	// else's file that nobody asked for.
	if !bytes.HasPrefix(out, []byte{0xEF, 0xBB, 0xBF}) {
		t.Error("the BOM the editor put there was silently removed")
	}
}
