package goadapter

import (
	"fmt"
	"testing"
)

func TestGoLeavesRequireTheSelectedModulesOwnDeclaration(t *testing.T) {
	const modulePath = "github.com/google/go-cmp"
	for _, tc := range []struct {
		name, version, body string
		want                int
	}{
		{"leaf", "v0.5.9", "module github.com/google/go-cmp\ngo 1.13\n", 1},
		{"synthetic-mod", "v0.5.9", "module github.com/google/go-cmp\n", 0},
		{"incompatible-synthetic-mod", "v2.0.0+incompatible", "module github.com/google/go-cmp\n", 0},
		{"incompatible-with-go-directive", "v2.0.0+incompatible", "module github.com/google/go-cmp\ngo 1.13\n", 0},
		{"missing-cache", "v0.5.9", "", 0},
		{"unresolved-child", "v0.5.9", "module github.com/google/go-cmp\ngo 1.13\nrequire example.com/child v1.0.0\n", 0},
		{"indirect-child", "v0.5.9", "module github.com/google/go-cmp\ngo 1.13\nrequire example.com/child v1.0.0 // indirect\n", 0},
		{"wrong-module", "v0.5.9", "module example.com/other\ngo 1.13\n", 0},
		{"malformed", "v0.5.9", "module github.com/google/go-cmp\nrequire (\n", 0},
		// A dependency's replace directives do not apply to the main module.
		{"ignored-dependency-replacement", "v0.5.9", "module github.com/google/go-cmp\ngo 1.13\nreplace example.com/old => ../local\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeBuildList(t, dir, fmt.Sprintf(`{"Path":%q,"Version":%q}`, modulePath, tc.version))
			if tc.body != "" {
				writeModuleGoMod(t, dir, modulePath, tc.version, tc.body)
			}
			got, err := New().ScanNoDependencies(t.Context(), dir)
			if err != nil || len(got) != tc.want {
				t.Fatalf("leaves=%v err=%v want=%d", got, err, tc.want)
			}
			if tc.want == 1 {
				wantPURL := "pkg:golang/" + modulePath + "@" + tc.version
				if got[0].String() != wantPURL {
					t.Fatalf("leaf=%s want=%s", got[0].String(), wantPURL)
				}
			}
		})
	}
	if got, err := New().ScanNoDependencies(t.Context(), t.TempDir()); err == nil || len(got) != 0 {
		t.Fatalf("missing build list produced facts: %v %v", got, err)
	}
}
