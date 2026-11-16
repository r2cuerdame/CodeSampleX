package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestBundleDependenciesJSONSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, entryFields string
		want              []string
	}{
		{"bundle-false", `"bundleDependencies":false`, []string{"pkg:npm/a@1.2.3"}},
		{"bundle-empty-array", `"bundleDependencies":[]`, []string{"pkg:npm/a@1.2.3"}},
		{"bundle-null", `"bundleDependencies":null`, []string{"pkg:npm/a@1.2.3"}},
		{"bundled-false", `"bundledDependencies":false`, []string{"pkg:npm/a@1.2.3"}},
		{"bundled-empty-array", `"bundledDependencies":[]`, []string{"pkg:npm/a@1.2.3"}},
		{"bundled-null", `"bundledDependencies":null`, []string{"pkg:npm/a@1.2.3"}},
		{"both-empty-or-false", `"bundleDependencies":[],"bundledDependencies":false`, []string{"pkg:npm/a@1.2.3"}},
		{"bundle-true", `"bundleDependencies":true`, nil},
		{"bundle-nonempty", `"bundleDependencies":["child"]`, nil},
		{"bundle-unknown-object", `"bundleDependencies":{}`, nil},
		{"bundle-unknown-string", `"bundleDependencies":"child"`, nil},
		{"bundled-true", `"bundledDependencies":true`, nil},
		{"bundled-nonempty", `"bundledDependencies":["child"]`, nil},
		{"bundled-unknown-number", `"bundledDependencies":1`, nil},
		{"mixed-false-and-nonempty", `"bundleDependencies":false,"bundledDependencies":["child"]`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := `{"lockfileVersion":3,"packages":{"":{"version":"1.0.0"},"node_modules/a":{"version":"1.2.3",` + tc.entryFields + `}}}`
			if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			got := scanLeafKeys(t, dir)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("leaves = %v, want %v", got, tc.want)
			}
		})
	}

	if hasBundledDependencies(nil) || hasBundledDependencies(json.RawMessage("   ")) {
		t.Fatal("empty/whitespace RawMessage should not block")
	}
	if !hasBundledDependencies(json.RawMessage("{")) {
		t.Fatal("invalid JSON RawMessage should conservatively block")
	}
}

func TestNoDependenciesExcludesNonRegistryInstalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		lock string
		want []string
	}{
		{
			name: "git-resolved-solo",
			lock: `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"a":"git+https://github.com/org/a.git#abc"}},
				"node_modules/a":{"version":"1.2.3","resolved":"git+https://github.com/org/a.git#abc"}
			}}`,
		},
		{
			name: "git-spec-without-resolved",
			lock: `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"a":"git+ssh://git@github.com/org/a.git#abc"}},
				"node_modules/a":{"version":"1.2.3"}
			}}`,
		},
		{
			name: "file-resolved-solo",
			lock: `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"a":"file:../a.tgz"}},
				"node_modules/a":{"version":"1.2.3","resolved":"file:../a.tgz"}
			}}`,
		},
		{
			name: "file-spec-without-resolved",
			lock: `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"a":"file:../a.tgz"}},
				"node_modules/a":{"version":"1.2.3"}
			}}`,
		},
		{
			name: "tarball-url-root-dep",
			lock: `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"a":"https://example.com/a-1.2.3.tgz"}},
				"node_modules/a":{"version":"1.2.3","resolved":"https://example.com/a-1.2.3.tgz"}
			}}`,
		},
		{
			name: "tarball-url-root-dev-dep",
			lock: `{"lockfileVersion":3,"packages":{
				"":{"devDependencies":{"a":"https://example.com/a-1.2.3.tgz"}},
				"node_modules/a":{"version":"1.2.3","dev":true,"resolved":"https://example.com/a-1.2.3.tgz"}
			}}`,
		},
		{
			name: "tarball-url-installed-optional-and-peer",
			lock: `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"p":"^1.0.0"}},
				"node_modules/p":{"version":"1.0.0","optionalDependencies":{"opt":"https://example.com/opt-1.0.0.tgz"},"peerDependencies":{"peer":"http://example.com/peer-1.0.0.tgz"}},
				"node_modules/opt":{"version":"1.0.0"},
				"node_modules/peer":{"version":"1.0.0"}
			}}`,
		},
		{
			name: "coexisting-fork-without-deps-and-public-with-deps",
			lock: `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"a":"https://example.com/a-1.2.3.tgz","b":"^1.0.0"}},
				"node_modules/a":{"version":"1.2.3","resolved":"https://example.com/a-1.2.3.tgz"},
				"node_modules/b":{"version":"1.0.0","dependencies":{"a":"^1.2.3"}},
				"node_modules/b/node_modules/a":{"version":"1.2.3","resolved":"https://registry.npmjs.org/a/-/a-1.2.3.tgz","dependencies":{"real-dep":"^1.0.0"}}
			}}`,
		},
		{
			name: "coexisting-fork-with-deps-and-public-without-deps",
			lock: `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"a":"git+https://github.com/fork/a.git#abc","b":"^1.0.0"}},
				"node_modules/a":{"version":"1.2.3","resolved":"git+https://github.com/fork/a.git#abc","dependencies":{"fork-dep":"^1.0.0"}},
				"node_modules/b":{"version":"1.0.0","dependencies":{"a":"^1.2.3"}},
				"node_modules/b/node_modules/a":{"version":"1.2.3","resolved":"https://registry.npmjs.org/a/-/a-1.2.3.tgz"}
			}}`,
			want: []string{"pkg:npm/a@1.2.3"},
		},
		{
			name: "aliased-non-registry-copy-isolated",
			lock: `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"my-a":"https://example.com/a-1.2.3.tgz","b":"^1.0.0"}},
				"node_modules/my-a":{"name":"a","version":"1.2.3","resolved":"https://example.com/a-1.2.3.tgz"},
				"node_modules/b":{"version":"1.0.0","dependencies":{"a":"^1.2.3"}},
				"node_modules/b/node_modules/a":{"version":"1.2.3","resolved":"https://registry.npmjs.org/a/-/a-1.2.3.tgz","dependencies":{"real-dep":"^1.0.0"}}
			}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(tc.lock), 0600); err != nil {
				t.Fatal(err)
			}
			got := scanLeafKeys(t, dir)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("leaves = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("workspace-member-tarball-url", func(t *testing.T) {
		root := tempTree(t, map[string]string{
			"package.json":          `{"name":"mono","workspaces":["apps/*"]}`,
			"apps/web/package.json": `{"name":"web"}`,
			"package-lock.json": `{"lockfileVersion":3,"packages":{
				"":{"workspaces":["apps/*"]},
				"apps/web":{"name":"web","dependencies":{"a":"https://example.com/a-1.2.3.tgz"}},
				"node_modules/web":{"resolved":"apps/web","link":true},
				"node_modules/a":{"version":"1.2.3","resolved":"https://example.com/a-1.2.3.tgz"}
			}}`,
		})
		for _, dir := range []string{root, filepath.Join(root, "apps", "web")} {
			if got := scanLeafKeys(t, dir); len(got) != 0 {
				t.Fatalf("%s leaves = %v, want none for workspace URL tarball", dir, got)
			}
		}
	})
}
