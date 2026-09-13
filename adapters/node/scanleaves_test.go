package node

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNoDependenciesRequiresAnExplicitInstalledDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
		wantErr    bool
	}{
		{"leaf", `{"lockfileVersion":3,"packages":{"":{"version":"1.0.0"},"node_modules/a":{"version":"1.2.3"}}}`, 1, false},
		{"alias", `{"lockfileVersion":3,"packages":{"node_modules/alias":{"name":"actual","version":"1.2.3"}}}`, 1, false},
		{"missing-child", `{"lockfileVersion":3,"packages":{"node_modules/a":{"version":"1.2.3","dependencies":{"missing":"1.0.0"}}}}`, 0, false},
		{"optional", `{"lockfileVersion":3,"packages":{"node_modules/a":{"version":"1.2.3","optionalDependencies":{"missing":"1.0.0"}}}}`, 0, false},
		{"peer", `{"lockfileVersion":3,"packages":{"node_modules/a":{"version":"1.2.3","peerDependencies":{"missing":"1.0.0"}}}}`, 0, false},
		{"bundled", `{"lockfileVersion":3,"packages":{"node_modules/a":{"version":"1.2.3","bundleDependencies":["child"]}}}`, 0, false},
		{"linked", `{"lockfileVersion":3,"packages":{"node_modules/a":{"version":"1.2.3","link":true}}}`, 0, false},
		{"range", `{"lockfileVersion":3,"packages":{"node_modules/a":{"version":"^1.2.3"}}}`, 0, false},
		{"conflicting-copies", `{"lockfileVersion":3,"packages":{"node_modules/a":{"version":"1.2.3"},"node_modules/b/node_modules/a":{"version":"1.2.3","dependencies":{"missing":"1.0.0"}}}}`, 0, false},
		{"future-version", `{"lockfileVersion":99,"packages":{"node_modules/a":{"version":"1.2.3"}}}`, 0, true},
		{"unsupported", `{"lockfileVersion":1,"dependencies":{"a":{"version":"1.2.3"}}}`, 0, true},
		{"malformed", `{`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := (Adapter{}).ScanNoDependencies(t.Context(), dir)
			if (err != nil) != tc.wantErr || len(got) != tc.want {
				t.Fatalf("got=%v err=%v", got, err)
			}
			if tc.name == "alias" && got[0].Name != "actual" {
				t.Fatalf("wrong alias attribution: %v", got)
			}
		})
	}
	if got, err := (Adapter{}).ScanNoDependencies(t.Context(), t.TempDir()); err == nil || len(got) != 0 {
		t.Fatalf("missing lockfile claimed a leaf: %v %v", got, err)
	}
}
