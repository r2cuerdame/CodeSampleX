package rust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCargoLeavesRequireValidPublicDeclarations(t *testing.T) {
	const entry = "[[package]]\nname = 'itoa'\nversion = '1.0.15'\nsource = 'registry+https://github.com/rust-lang/crates.io-index'\n"
	for _, tc := range []struct {
		name, body string
		want       int
		wantErr    bool
	}{
		{"leaf", "version = 4\n" + entry, 1, false},
		{"empty-list", "version = 3\n" + entry + "dependencies = []\n", 1, false},
		{"unresolved-child", "version = 4\n" + entry + "dependencies = ['missing 1.0.0']\n", 0, false},
		{"private-source", "version = 4\n" + strings.ReplaceAll(entry, "registry+https://github.com/rust-lang/crates.io-index", "git+https://private.invalid/repo"), 0, false},
		{"conflicting-copies", "version = 4\n" + entry + entry + "dependencies = ['missing']\n", 0, false},
		{"wrong-type", "version = 4\n" + entry + "dependencies = {}\n", 0, true},
		{"malformed", "version = 4\n" + entry + "dependencies = [", 0, true},
		{"future-version", "version = 99\n" + entry, 0, true},
		{"no-version", entry, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "Cargo.lock"), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := (Adapter{}).ScanNoDependencies(t.Context(), dir)
			if len(got) != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("leaves=%v err=%v", got, err)
			}
		})
	}
}
