package cli

import (
	"context"
	"testing"
)

func TestDispatch(t *testing.T) {
	var gotArgs []string
	Register(Command{
		Name:    "test-dispatch-cmd",
		Summary: "test helper",
		Run: func(_ context.Context, args []string) int {
			gotArgs = args
			return 42
		},
	})

	if code := Main([]string{"test-dispatch-cmd", "a", "b"}); code != 42 {
		t.Fatalf("Main returned %d, want 42", code)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "a" || gotArgs[1] != "b" {
		t.Fatalf("args = %v", gotArgs)
	}
}

func TestDispatchUnknownAndHelp(t *testing.T) {
	if code := Main([]string{"no-such-command-xyz"}); code != 2 {
		t.Fatalf("unknown command returned %d, want 2", code)
	}
	if code := Main(nil); code != 0 {
		t.Fatalf("no-args usage returned %d, want 0", code)
	}
	if code := Main([]string{"help"}); code != 0 {
		t.Fatalf("help returned %d, want 0", code)
	}
}

func TestBuiltinCommandsRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, c := range Commands() {
		names[c.Name] = true
		if c.Summary == "" {
			t.Errorf("command %q has no summary", c.Name)
		}
	}
	for _, want := range []string{"run", "version"} {
		if !names[want] {
			t.Errorf("command %q not registered", want)
		}
	}
	// Sorted listing.
	cmds := Commands()
	for i := 1; i < len(cmds); i++ {
		if cmds[i-1].Name > cmds[i].Name {
			t.Fatalf("Commands() not sorted: %q > %q", cmds[i-1].Name, cmds[i].Name)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	if code := Main([]string{"version"}); code != 0 {
		t.Fatalf("version returned %d, want 0", code)
	}
}

func TestRunUsageError(t *testing.T) {
	t.Setenv("CSX_HOME", t.TempDir())
	if code := Main([]string{"run"}); code != 2 {
		t.Fatalf("bare `csx run` returned %d, want 2", code)
	}
	if code := Main([]string{"run", "--"}); code != 2 {
		t.Fatalf("`csx run --` returned %d, want 2", code)
	}
}
