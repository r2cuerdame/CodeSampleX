package goadapter

import "testing"

func TestGoLeavesRequireTheSelectedModulesOwnDeclaration(t *testing.T) {
	const modulePath = "github.com/google/go-cmp"
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"leaf", "module github.com/google/go-cmp\ngo 1.13\n", 1},
		{"missing-cache", "", 0},
		{"unresolved-child", "module github.com/google/go-cmp\nrequire example.com/child v1.0.0\n", 0},
		{"indirect-child", "module github.com/google/go-cmp\nrequire example.com/child v1.0.0 // indirect\n", 0},
		{"wrong-module", "module example.com/other\n", 0},
		{"malformed", "module github.com/google/go-cmp\nrequire (\n", 0},
		// A dependency's replace directives do not apply to the main module.
		{"ignored-dependency-replacement", "module github.com/google/go-cmp\nreplace example.com/old => ../local\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeBuildList(t, dir, `{"Path":"github.com/google/go-cmp","Version":"v0.5.9"}`)
			if tc.body != "" {
				writeModuleGoMod(t, dir, modulePath, "v0.5.9", tc.body)
			}
			got, err := New().ScanNoDependencies(t.Context(), dir)
			if err != nil || len(got) != tc.want {
				t.Fatalf("leaves=%v err=%v want=%d", got, err, tc.want)
			}
		})
	}
	if got, err := New().ScanNoDependencies(t.Context(), t.TempDir()); err == nil || len(got) != 0 {
		t.Fatalf("missing build list produced facts: %v %v", got, err)
	}
}
