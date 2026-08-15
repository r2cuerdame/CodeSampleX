package samples

import (
	"os"
	"path/filepath"
	"testing"
)

// ScanOptions.ProjectDirName and GitRemoteName are what the whole
// KindProjectName check is built on — "a sample mentioning either leaks
// provenance" — and they were never set. All three call sites passed
// ScanOptions{}, so the check compiled no patterns and matched nothing, at
// create time and at the publish gate alike. A sample carrying
// "part of the acme-billing-core monorepo" published clean.
func TestTheProjectNameCheckActuallyHasANameToCheck(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "acme-billing-core")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("csx.json", `{"schemaVersion":1}`)
	write("index.js", "// part of the acme-billing-core monorepo\nexport default 1\n")

	opts := ProvenanceOptions(dir)
	if opts.ProjectDirName != "acme-billing-core" {
		t.Fatalf("ProjectDirName = %q", opts.ProjectDirName)
	}
	findings, err := Scan(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	var caught bool
	for _, f := range findings {
		if f.Kind == KindProjectName {
			caught = true
		}
	}
	if !caught {
		t.Errorf("the project name was not flagged: %+v", findings)
	}
}

// A generic or short directory name identifies nobody, and treating one as
// the project name would flag the word everywhere it appears and reject
// every honest sample.
func TestAGenericDirectoryNameIsNotAProjectName(t *testing.T) {
	for _, name := range []string{"src", "app", "lib", "test", "examples", "tmp", "x"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := ProvenanceOptions(dir); got.ProjectDirName != "" {
			t.Errorf("%q was treated as a project name", got.ProjectDirName)
		}
	}
}
