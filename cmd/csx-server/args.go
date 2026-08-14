package main

import (
	"flag"
	"strings"
)

// reorderFlagsFirst moves every flag, with its value, ahead of the
// positional arguments.
//
// Go's flag package stops parsing at the first non-flag argument, so the
// documented operator form
//
//	csx-server quarantine sha256:… --reason "…"
//
// left --reason unset, counted three positionals, and printed a usage line
// showing exactly the command that had just been typed. An operator command
// used a handful of times a year is the worst place for an argument order
// that only works one way, so both orders work now.
func reorderFlagsFirst(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" { // everything after it is positional by definition
			pos = append(pos, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.ContainsRune(name, '=') {
			continue // --reason=… carries its own value
		}
		// A bool flag never consumes the next argument; anything else does.
		// An unknown flag is left alone so flag.Parse reports it.
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, pos...)
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}
