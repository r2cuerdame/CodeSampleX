// Package adapters wires every ecosystem adapter into one registry.
// It lives above internal/scanner so adapters can import the interface
// without a cycle.
package adapters

import (
	"github.com/r2cuerdame/codesamplex/adapters/goadapter"
	"github.com/r2cuerdame/codesamplex/adapters/node"
	"github.com/r2cuerdame/codesamplex/adapters/python"
	"github.com/r2cuerdame/codesamplex/adapters/rust"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// All returns every ecosystem adapter, in the order they are tried.
// Node/TS is the reference ecosystem and goes first (goal.md §13.2).
func All() []scanner.Adapter {
	return []scanner.Adapter{node.Adapter{}, python.New(), goadapter.New(), rust.New()}
}

// Detect returns the adapters whose ecosystems are present in dir.
// A polyglot repo can match several.
func Detect(dir string) []scanner.Adapter {
	var found []scanner.Adapter
	for _, a := range All() {
		if a.Detect(dir) {
			found = append(found, a)
		}
	}
	return found
}
