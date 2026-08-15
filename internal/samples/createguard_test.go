package samples

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A sample that can be CREATED but never previewed, verified or published
// is a dead end with no way out: the row is in the local store, the
// artifact is in the CAS, and every command that touches it fails forever.
//
// Only the compressed size was checked at creation. Unpack enforces three
// more rules, so a 9MB fixture that compresses to 32KB sailed through and
// then failed everywhere after.
func TestCreateRefusesWhatUnpackWouldRefuse(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		body    string
		wantSay string
	}{
		{
			name:    "a file larger than a sample may carry",
			file:    "fixture.txt",
			body:    strings.Repeat("a", maxUnpackedBytes+1),
			wantSay: "at most",
		},
		{
			name:    "a name with a colon, as an ISO timestamp has",
			file:    "fixtures/2024-01-02T03:04:05Z.json",
			body:    "{}",
			wantSay: "colon",
		},
		{
			name:    "a non-ASCII name",
			file:    "tests/café_fixture.json",
			body:    "{}",
			wantSay: "ASCII-only",
		},
		{
			name:    "a path component past the archive limit",
			file:    strings.Repeat("a", 113) + ".ts",
			body:    "x",
			wantSay: "Shorten it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			full := filepath.Join(dir, filepath.FromSlash(tc.file))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Skipf("this filesystem cannot hold %q: %v", tc.file, err)
			}
			if err := os.WriteFile(full, []byte(tc.body), 0o600); err != nil {
				t.Skipf("this filesystem cannot hold %q: %v", tc.file, err)
			}
			if err := os.WriteFile(filepath.Join(dir, "csx.json"), []byte(`{"schemaVersion":1}`), 0o600); err != nil {
				t.Fatal(err)
			}

			_, _, err := BuildArtifact(dir)
			if err == nil {
				t.Fatalf("created a sample that could never be published: %s", tc.file)
			}
			if !strings.Contains(err.Error(), tc.wantSay) {
				t.Errorf("the refusal does not say what to do: %v", err)
			}
		})
	}
}

// An ordinary sample is untouched.
func TestCreateStillAcceptsAnOrdinarySample(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "csx.json"), []byte(`{"schemaVersion":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test", "contract.mjs"), []byte("console.log('ok')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tgz, id, err := BuildArtifact(dir)
	if err != nil {
		t.Fatalf("an ordinary sample was refused: %v", err)
	}
	if id == "" || len(tgz) == 0 {
		t.Fatal("empty artifact")
	}
	// And what create accepts, unpack takes.
	out := t.TempDir()
	if err := Unpack(tgz, out); err != nil {
		t.Errorf("create produced something unpack refuses: %v", err)
	}
}

// The name rules are checked directly too: a colon is legal on Linux and
// macOS — where most contributors are — but Windows cannot even create such
// a file, so the case above skips there and would otherwise go uncovered on
// the machine this is usually developed on.
func TestEntryNameRulesHoldOnEveryPlatform(t *testing.T) {
	bad := map[string]string{
		"fixtures/2024-01-02T03:04:05Z.json": "colon",
		`src/a\b_test.go`:                    "backslash",
		"tests/café.json":                    "ASCII-only",
		strings.Repeat("x", 101) + ".ts":     "Shorten it",
	}
	for name, want := range bad {
		err := buildableEntryName(name)
		if err == nil {
			t.Errorf("%q was accepted; unpack would refuse it", name)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: refusal does not mention %q: %v", name, want, err)
		}
	}
	for _, ok := range []string{"csx.json", "test/contract.mjs", "src/index.ts", strings.Repeat("x", 100)} {
		if err := buildableEntryName(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}
