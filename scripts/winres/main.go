// Command winres writes the Windows version resource and manifest objects
// (rsrc_windows_<arch>.syso) into cmd/csx and cmd/csx-launcher before a
// Windows build. Run it from the repository root:
//
//	go run ./scripts/winres -version v0.1.105
//
// The objects are build outputs and are not committed; a build that skips
// this step still works and simply carries no version resource.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/winres"
)

func main() {
	version := flag.String("version", "", "release version, e.g. v0.1.105 (required)")
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if *version == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/winres -version <version> [-root <dir>]")
		os.Exit(2)
	}
	if err := run(*root, *version); err != nil {
		fmt.Fprintln(os.Stderr, "winres:", err)
		os.Exit(1)
	}
}

func run(root, version string) error {
	bins := winres.Binaries(version)
	pkgs := make([]string, 0, len(bins))
	for pkg := range bins {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		dir := filepath.Join(root, filepath.FromSlash(pkg))
		if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
			return fmt.Errorf("%s is not a main package here: %w", pkg, err)
		}
		for _, arch := range winres.Arches {
			obj, err := winres.Syso(arch, bins[pkg])
			if err != nil {
				return fmt.Errorf("%s %s: %w", pkg, arch, err)
			}
			out := filepath.Join(dir, winres.SysoName(arch))
			if err := os.WriteFile(out, obj, 0o644); err != nil {
				return err
			}
			fmt.Println("wrote", filepath.ToSlash(filepath.Join(pkg, winres.SysoName(arch))))
		}
	}
	return nil
}
