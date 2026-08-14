module codesamplex.dev/sample/gocobra

go 1.24

require github.com/spf13/cobra v1.10.2

// pflag sits one release above what cobra v1.10.2 asks for. Minimal version
// selection settles on pflag v1.0.9, which is what cobra's own go.mod
// requires; v1.0.10 is the latest release and was measured here to produce the
// identical error text this contract pins ("unknown flag: --nope" and
// "no such flag -config"). Both of those errors come from pflag rather than
// cobra, which is why its version is worth stating rather than inheriting.
require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
)
