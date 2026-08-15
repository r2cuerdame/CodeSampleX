package samples

import (
	"os"
	"path/filepath"
	"testing"
)

// requirements.txt was exempted from the URL host allowlist as a
// "machine-generated dependency manifest written by the package manager
// rather than by the contributor". It is hand-authored, and it is exactly
// where a private index gets named — so an employer's internal package
// host published unflagged, while the identical line in a shell script was
// caught.
func TestAPrivateIndexInRequirementsIsAFinding(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("csx.json", `{"schemaVersion":1}`)
	write("requirements.txt", "--index-url https://pypi.internal.acmecorp.io/simple\nrequests==2.31.0\n")

	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var caught bool
	for _, f := range findings {
		if f.Kind == KindURL {
			caught = true
		}
	}
	if !caught {
		t.Errorf("an internal package index published unflagged: %+v", findings)
	}
}

// An honest requirements.txt still passes: the public indexes are on the
// host allowlist, and a plain pinned dependency names no host at all.
func TestAnOrdinaryRequirementsFilePasses(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"csx.json":         `{"schemaVersion":1}`,
		"requirements.txt": "--index-url https://pypi.org/simple\nrequests==2.31.0\nurllib3==2.2.1\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("an ordinary requirements.txt was flagged: %+v", findings)
	}
}
