package python

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonLeavesRequireValidLockDeclarations(t *testing.T) {
	const uv = "version = 1\n[[package]]\nname = 'idna'\nversion = '3.10'\nsource = {registry = 'https://pypi.org/simple'}\n"
	const poetry = "[metadata]\nlock-version = '2.1'\n[[package]]\nname = 'idna'\nversion = '3.10'\n"
	for _, tc := range []struct {
		name, file, body string
		want             int
		wantErr          bool
	}{
		{"uv-leaf", "uv.lock", uv, 1, false},
		{"uv-empty-list", "uv.lock", uv + "dependencies = []\n", 1, false},
		{"uv-unresolved-child", "uv.lock", uv + "dependencies = [{name = 'missing'}]\n", 0, false},
		{"uv-optional", "uv.lock", uv + "[package.optional-dependencies]\nextra = [{name = 'missing'}]\n", 0, false},
		{"uv-dev", "uv.lock", uv + "[package.dev-dependencies]\ntest = [{name = 'missing'}]\n", 0, false},
		{"uv-private", "uv.lock", strings.ReplaceAll(uv, "https://pypi.org/simple", "https://private.invalid/simple"), 0, false},
		{"uv-wrong-type", "uv.lock", uv + "dependencies = {}\n", 0, true},
		{"uv-malformed", "uv.lock", uv + "dependencies = [", 0, true},
		{"uv-future-version", "uv.lock", strings.Replace(uv, "version = 1", "version = 99", 1), 0, true},
		{"uv-conflicting-copies", "uv.lock", uv + strings.TrimPrefix(uv, "version = 1\n") + "dependencies = [{name = 'missing'}]\n", 0, false},
		{"poetry-leaf", "poetry.lock", poetry, 1, false},
		{"poetry-empty-table", "poetry.lock", poetry + "[package.dependencies]\n", 1, false},
		{"poetry-unresolved-child", "poetry.lock", poetry + "[package.dependencies]\nmissing = '^1.0'\n", 0, false},
		{"poetry-extras", "poetry.lock", poetry + "[package.extras]\ntest = ['missing']\n", 0, false},
		{"poetry-private", "poetry.lock", poetry + "[package.source]\ntype = 'git'\nurl = 'https://private.invalid/repo'\n", 0, false},
		{"poetry-wrong-type", "poetry.lock", poetry + "dependencies = []\n", 0, true},
		{"poetry-no-version", "poetry.lock", "[[package]]\nname = 'idna'\nversion = '3.10'\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := New().ScanNoDependencies(t.Context(), dir)
			if len(got) != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("leaves=%v err=%v", got, err)
			}
		})
	}
}
