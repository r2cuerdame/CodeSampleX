package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	sample "codesamplex.dev/sample/gocmp/src"
)

func fail(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}

func main() {
	t1 := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	a := sample.NewPeer("ed25519:aaa", 48620, t1, "x")
	b := sample.NewPeer("ed25519:aaa", 48620, t2, "x")

	// The timestamps differ, but Diff ignores that field.
	if d := sample.Diff(a, b); d != "" {
		fail("ignored field still reported:\n%s", d)
	}
	// Comparing it strictly does report the difference.
	if d := sample.DiffStrict(a, b); d == "" {
		fail("strict diff should report the timestamp difference")
	}

	// A real difference is reported with the field name in it.
	c := sample.NewPeer("ed25519:bbb", 48620, t1, "x")
	d := sample.Diff(a, c)
	if d == "" || !strings.Contains(d, "ID") {
		fail("expected a readable diff naming ID, got %q", d)
	}

	// Without an option, unexported fields panic instead of being ignored.
	if _, panicked := sample.DiffDefault(a, c); !panicked {
		fail("cmp should panic on unexported fields unless allowed")
	}

	fmt.Println("CONTRACT PASS: go-cmp diffed, ignored and panicked exactly as documented")
}
