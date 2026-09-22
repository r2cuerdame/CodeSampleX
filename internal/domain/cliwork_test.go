package domain

import "testing"

// The authoring queue keys work by (ecosystem, name, version, symbol) and
// nothing else. A CLI coordinate has one more axis -- the OS the command ran
// on -- so the OS is carried inside the symbol. Both stores, the ledger and
// the worker read the same encoding.
func TestCLIWorkSymbolRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		os, command, want, wantOS, wantCommand string
	}{
		{"linux", "worktree add <path>", "[linux] worktree add <path>", "linux", "worktree add <path>"},
		{"linux", "", "[linux]", "linux", ""},
		{"Windows", "  compose up   -d ", "[windows] compose up -d", "windows", "compose up -d"},
	} {
		got := EncodeCLIWorkSymbol(tc.os, tc.command)
		if got != tc.want {
			t.Errorf("EncodeCLIWorkSymbol(%q,%q) = %q, want %q", tc.os, tc.command, got, tc.want)
		}
		os, command, ok := DecodeCLIWorkSymbol(got)
		if !ok || os != tc.wantOS || command != tc.wantCommand {
			t.Errorf("DecodeCLIWorkSymbol(%q) = %q,%q,%v", got, os, command, ok)
		}
	}
}

func TestDecodeCLIWorkSymbolRefusesOtherSymbols(t *testing.T) {
	for _, symbol := range []string{"", "run", "farm:worktree add", "[]", "[linux", "[ linux ] x", "[Linux-x64] x"} {
		if _, _, ok := DecodeCLIWorkSymbol(symbol); ok {
			t.Errorf("DecodeCLIWorkSymbol(%q) accepted a symbol that is not a CLI work coordinate", symbol)
		}
	}
}

func TestCLIToolFromTargetName(t *testing.T) {
	if tool, ok := CLIToolFromTargetName("cli/git"); !ok || tool != "git" {
		t.Fatalf("cli/git -> %q,%v", tool, ok)
	}
	for _, name := range []string{"engine/unity", "git", "cli/", "cli/not-a-tool"} {
		if _, ok := CLIToolFromTargetName(name); ok {
			t.Errorf("%q is not a public CLI target", name)
		}
	}
}

func TestCLIWorkCommandPlaceholders(t *testing.T) {
	if !CLIWorkHasPlaceholders("worktree add <path>") || CLIWorkHasPlaceholders("status --short") || CLIWorkHasPlaceholders("") {
		t.Fatal("placeholder detection is wrong")
	}
}

// The seed order is the priority the farm fills first; it is a fixed list so
// the planner, the panel and the docs agree on it.
func TestCLISeedToolsArePublicTargetsInPriorityOrder(t *testing.T) {
	want := []string{"gh", "git", "docker", "powershell", "bash", "go", "npm", "pnpm", "cargo"}
	if len(CLISeedTools) != len(want) {
		t.Fatalf("CLISeedTools = %v, want %v", CLISeedTools, want)
	}
	for i, tool := range want {
		if CLISeedTools[i] != tool {
			t.Fatalf("CLISeedTools[%d] = %q, want %q", i, CLISeedTools[i], tool)
		}
		if !IsRecognizedCLITool(tool) {
			t.Fatalf("seed %q is not a recognized public CLI tool", tool)
		}
		if rank, ok := CLISeedRank(tool); !ok || rank != i {
			t.Fatalf("CLISeedRank(%q) = %d,%v", tool, rank, ok)
		}
	}
	if _, ok := CLISeedRank("jq"); ok {
		t.Fatal("jq is not a seed")
	}
}
