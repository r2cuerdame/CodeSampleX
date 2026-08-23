package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tool list is published in three places a reader can act on — the
// README, the agent-directed install guide, and the directory listings both
// of those feed — and in exactly one place it is true: toolDefs(). A tool
// added or renamed without touching the prose leaves a public document
// naming a tool that does not answer, which is worse than naming none.
//
// The count is spelled out in words in the README ("Eight tools:"), so it
// cannot even be grepped for a digit. This test is what ties it down.

func docsRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

func readDoc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(docsRepoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

// spelledCounts covers the range a tool list can plausibly reach. The README
// writes the number as a word, so the test has to read words.
var spelledCounts = map[int]string{
	5: "Five", 6: "Six", 7: "Seven", 8: "Eight", 9: "Nine",
	10: "Ten", 11: "Eleven", 12: "Twelve",
}

func TestTheREADMENamesEveryToolTheServerRegisters(t *testing.T) {
	defs := toolDefs()
	readme := readDoc(t, "README.md")

	for _, d := range defs {
		if !strings.Contains(readme, "`"+d.Name+"`") {
			t.Errorf("README.md does not name the registered tool %s", d.Name)
		}
	}

	word, ok := spelledCounts[len(defs)]
	if !ok {
		t.Fatalf("the server registers %d tools, which spelledCounts has no word for", len(defs))
	}
	if !strings.Contains(readme, word+" tools:") {
		t.Errorf("README.md does not say %q; the server registers %d tools", word+" tools:", len(defs))
	}
	for n, other := range spelledCounts {
		if n != len(defs) && strings.Contains(readme, other+" tools:") {
			t.Errorf("README.md still says %q while the server registers %d", other+" tools:", len(defs))
		}
	}
}

// Publishing is deliberately not an MCP capability: only a human at the CLI
// can upload sample source. A tool named for it would contradict the promise
// the README makes two sentences later, so the absence is part of the
// contract and not an oversight to be corrected.
func TestNoToolCanPublish(t *testing.T) {
	for _, d := range toolDefs() {
		if strings.Contains(d.Name, "publish") {
			t.Errorf("a tool named %s exists; the README promises no publish tool", d.Name)
		}
	}
	if !strings.Contains(readDoc(t, "README.md"), "no publish tool") {
		t.Error("README.md no longer states that there is no publish tool")
	}
}
