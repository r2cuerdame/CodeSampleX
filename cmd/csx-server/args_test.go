package main

import (
	"flag"
	"reflect"
	"testing"
)

// The bug this guards: `quarantine <id> --reason "…"` is the documented
// order, and Go's flag package stops at <id>, so --reason never parsed.
func TestReorderFlagsFirst(t *testing.T) {
	newFS := func() *flag.FlagSet {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.String("reason", "", "")
		fs.Bool("release", false, "")
		return fs
	}
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"documented order", []string{"sha256:x", "--reason", "a b"}, []string{"--reason", "a b", "sha256:x"}},
		{"flags first already", []string{"--reason", "a b", "sha256:x"}, []string{"--reason", "a b", "sha256:x"}},
		{"bool flag takes no value", []string{"sha256:x", "--release"}, []string{"--release", "sha256:x"}},
		{"inline value", []string{"sha256:x", "--reason=a"}, []string{"--reason=a", "sha256:x"}},
		{"single dash form", []string{"sha256:x", "-reason", "a"}, []string{"-reason", "a", "sha256:x"}},
		{"double dash stops parsing", []string{"--release", "--", "-not-a-flag"}, []string{"--release", "-not-a-flag"}},
		{"unknown flag is left for Parse to reject", []string{"sha256:x", "--nope"}, []string{"--nope", "sha256:x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reorderFlagsFirst(newFS(), c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}

	// End to end: the documented order must actually set the flag.
	fs := newFS()
	if err := fs.Parse(reorderFlagsFirst(fs, []string{"sha256:x", "--reason", "because"})); err != nil {
		t.Fatal(err)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "sha256:x" {
		t.Fatalf("positional = %q", fs.Args())
	}
	if got := fs.Lookup("reason").Value.String(); got != "because" {
		t.Fatalf("reason = %q", got)
	}
}
