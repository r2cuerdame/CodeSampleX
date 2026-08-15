package samples

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sampleFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "csx.json", `{"schemaVersion":1}`)
	writeFile(t, dir, "src/echo.mjs", "export function echo(x){ return x }\n")
	writeFile(t, dir, "test/contract.mjs", "console.log('ok')\n")
	return dir
}

func TestBuildArtifactDeterminism(t *testing.T) {
	dir := sampleFixture(t)
	tgz1, id1, err := BuildArtifact(dir)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if !strings.HasPrefix(id1, "sha256:") {
		t.Fatalf("sample id %q lacks sha256: prefix", id1)
	}
	if id1 != domain.SHA256Hex(tgz1) {
		t.Fatalf("sample id %q != hash of tgz %q", id1, domain.SHA256Hex(tgz1))
	}

	// Touch every file mtime; the canonical artifact must not change.
	later := time.Now().Add(90 * time.Minute)
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		return os.Chtimes(p, later, later)
	})
	if err != nil {
		t.Fatal(err)
	}

	tgz2, id2, err := BuildArtifact(dir)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("artifact not deterministic: %s != %s", id1, id2)
	}
	if !bytes.Equal(tgz1, tgz2) {
		t.Fatal("artifact bytes differ across builds")
	}
}

func TestBuildArtifactTarShapeCanonical(t *testing.T) {
	dir := sampleFixture(t)
	tgz, _, err := BuildArtifact(dir)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}
	if gz.Header.Name != "" {
		t.Fatalf("gzip name should be empty, got %q", gz.Header.Name)
	}
	if !gz.Header.ModTime.IsZero() && gz.Header.ModTime.Unix() != 0 {
		t.Fatalf("gzip mtime not zero: %v", gz.Header.ModTime)
	}
	tr := tar.NewReader(gz)
	var prev string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name <= prev {
			t.Fatalf("entries not strictly sorted: %q after %q", hdr.Name, prev)
		}
		prev = hdr.Name
		if hdr.Mode != 0o644 {
			t.Fatalf("entry %s mode %o, want 0644", hdr.Name, hdr.Mode)
		}
		if hdr.Uid != 0 || hdr.Gid != 0 || hdr.Uname != "" || hdr.Gname != "" {
			t.Fatalf("entry %s carries uid/gid/user info", hdr.Name)
		}
		if hdr.ModTime.Unix() != 0 {
			t.Fatalf("entry %s mtime %v, want epoch 0", hdr.Name, hdr.ModTime)
		}
		if hdr.Format == tar.FormatPAX || len(hdr.PAXRecords) > 0 {
			t.Fatalf("entry %s uses PAX headers", hdr.Name)
		}
	}
}

func TestBuildArtifactRejectsForbiddenEntries(t *testing.T) {
	cases := []string{
		".git/config",
		".env",
		"config/.env",
	}
	for _, rel := range cases {
		t.Run(rel, func(t *testing.T) {
			dir := sampleFixture(t)
			writeFile(t, dir, rel, "data")
			if _, _, err := BuildArtifact(dir); err == nil {
				t.Fatalf("expected rejection for %s", rel)
			}
		})
	}
}

// `csx sample check` mounts the sample dir as the sandbox workspace, so a
// check leaves .csx-vendor/ behind and the toolchain leaves target/, deps/,
// vendor/ next to it. Refusing on those made the documented workflow —
// check, then create — fail on its own leftovers, naming a directory the
// user never made with no hint that deleting it was safe. They are skipped
// instead: the artifact is the same either way, and the sample ID no longer
// depends on whether a check happened to run first.
func TestBuildArtifactSkipsWhatRunningTheCheckLeavesBehind(t *testing.T) {
	for _, rel := range []string{
		"node_modules/x/index.js",
		"target/debug/a.txt",
		"dist/bundle.js",
		".csx-vendor/cargo/x.crate",
		"vendor/autoload.php",
		"deps/plug/mix.exs",
		"_build/dev/a.beam",
		".dart_tool/package_config.json",
		".bundle/config",
		"src/__pycache__/mod.cpython-312.pyc",
		".venv/lib/python3.13/site-packages/x.py",
	} {
		t.Run(rel, func(t *testing.T) {
			dir := sampleFixture(t)
			clean, cleanID, err := BuildArtifact(dir)
			if err != nil {
				t.Fatalf("clean tree: %v", err)
			}
			writeFile(t, dir, rel, "data")
			dirty, dirtyID, err := BuildArtifact(dir)
			if err != nil {
				t.Fatalf("%s should be skipped, not refused: %v", rel, err)
			}
			if dirtyID != cleanID {
				t.Errorf("%s changed the sample ID: %s vs %s", rel, dirtyID, cleanID)
			}
			if len(dirty) != len(clean) {
				t.Errorf("%s changed the artifact", rel)
			}
			for _, n := range tarNames(t, dirty) {
				if n == rel {
					t.Errorf("%s reached the artifact", rel)
				}
			}
		})
	}
}

// Skipping is only safe because nothing can smuggle these in from
// elsewhere: an artifact built by other code, or by hand, is still refused
// on arrival.
func TestUnpackStillRefusesWhatBuildArtifactSkips(t *testing.T) {
	for _, name := range []string{
		"node_modules/x.js",
		"target/debug/a.txt",
		"vendor/autoload.php",
		".csx-vendor/x.crate",
		"deps/plug/mix.exs",
		"src/__pycache__/mod.pyc",
		"a.pyc",
	} {
		t.Run(name, func(t *testing.T) {
			if err := Unpack(makeTgz(t, name, "x", tar.TypeReg), t.TempDir()); err == nil {
				t.Errorf("unpack accepted %s", name)
			}
		})
	}
}

func TestBuildArtifactRejectsBinary(t *testing.T) {
	dir := sampleFixture(t)
	writeFile(t, dir, "blob.bin", "abc\x00def")
	if _, _, err := BuildArtifact(dir); err == nil {
		t.Fatal("expected rejection of NUL-byte binary file")
	}
}

func TestBuildArtifactRejectsTooManyFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i <= MaxFiles; i++ {
		writeFile(t, dir, fmt.Sprintf("f%03d.txt", i), "x")
	}
	if _, _, err := BuildArtifact(dir); err == nil {
		t.Fatalf("expected rejection above %d files", MaxFiles)
	}
}

func TestBuildArtifactRejectsOversize(t *testing.T) {
	dir := sampleFixture(t)
	raw := make([]byte, 480*1024)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "big.txt", base64.StdEncoding.EncodeToString(raw))
	if _, _, err := BuildArtifact(dir); err == nil {
		t.Fatal("expected rejection above compressed size limit")
	}
}

func TestBuildArtifactRejectsEmptyDir(t *testing.T) {
	if _, _, err := BuildArtifact(t.TempDir()); err == nil {
		t.Fatal("expected rejection of empty sample dir")
	}
}

func TestBuildArtifactRejectsSymlink(t *testing.T) {
	dir := sampleFixture(t)
	if err := os.Symlink(filepath.Join(dir, "csx.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if _, _, err := BuildArtifact(dir); err == nil {
		t.Fatal("expected rejection of symlink")
	}
}

func TestUnpackRoundTrip(t *testing.T) {
	dir := sampleFixture(t)
	tgz, id, err := BuildArtifact(dir)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := Unpack(tgz, dest); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "src", "echo.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "export function echo(x){ return x }\n" {
		t.Fatalf("unpacked content mismatch: %q", got)
	}
	// Rebuilding from the unpacked tree reproduces the same sample id.
	_, id2, err := BuildArtifact(dest)
	if err != nil {
		t.Fatal(err)
	}
	if id != id2 {
		t.Fatalf("unpack→rebuild id mismatch: %s != %s", id, id2)
	}
}

func makeTgz(t *testing.T, name, content string, typeflag byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: typeflag}
	if typeflag == tar.TypeSymlink {
		hdr.Size = 0
		hdr.Linkname = "csx.json"
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if typeflag == tar.TypeReg {
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUnpackRejectsUnsafeEntries(t *testing.T) {
	cases := []struct {
		name     string
		entry    string
		typeflag byte
	}{
		{"traversal", "../evil.txt", tar.TypeReg},
		{"nested traversal", "a/../../evil.txt", tar.TypeReg},
		{"absolute", "/abs.txt", tar.TypeReg},
		{"backslash", `..\evil.txt`, tar.TypeReg},
		{"drive colon", `C:\evil.txt`, tar.TypeReg},
		{"forbidden dir", "node_modules/x.js", tar.TypeReg},
		{"env file", ".env", tar.TypeReg},
		{"symlink entry", "link", tar.TypeSymlink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgz := makeTgz(t, tc.entry, "data", tc.typeflag)
			if err := Unpack(tgz, t.TempDir()); err == nil {
				t.Fatalf("expected rejection of entry %q", tc.entry)
			}
		})
	}
}

func TestUnpackRejectsOversize(t *testing.T) {
	// Compressed cap.
	if err := Unpack(make([]byte, MaxCompressedBytes+1), t.TempDir()); err == nil {
		t.Fatal("expected rejection above compressed cap")
	}
	// Decompression-bomb cap: zeros compress tiny but expand past the limit.
	tgz := makeTgz(t, "zeros.txt", strings.Repeat("\x00", 9<<20), tar.TypeReg)
	if err := Unpack(tgz, t.TempDir()); err == nil {
		t.Fatal("expected rejection above unpacked size cap")
	}
}

// Running a contract inside a seed directory leaves src/__pycache__ behind,
// and it was only refused at the root. Bytecode names the interpreter that
// wrote it — cpython-310 on the publishing machine, in a sample whose
// environment says python 3.12 — so it is host detail in a document meant
// to carry none. Refuse it by name rather than relying on the binary check
// noticing NUL bytes in a .pyc.
func TestBuildArtifactRejectsCompiledOutput(t *testing.T) {
	for _, rel := range []string{
		"src/__pycache__/mod.cpython-312.pyc",
		"__pycache__/mod.pyc",
		"build/Thing.class",
		".phpunit.result.cache",
	} {
		t.Run(rel, func(t *testing.T) {
			dir := t.TempDir()
			writeArtifactFile(t, dir, "main.py", "print('hi')\n")
			writeArtifactFile(t, dir, rel, "not really compiled\n")
			tgz, _, err := BuildArtifact(dir)
			if err != nil {
				t.Fatalf("%s should be skipped, not refused: %v", rel, err)
			}
			for _, n := range tarNames(t, tgz) {
				if n == rel {
					t.Fatalf("%s was accepted into the artifact", rel)
				}
			}
		})
	}
}

// The root-only rule really is root-only: a sample may ship src/vendor/.
func TestBuildArtifactAllowsNestedVendorButNotRoot(t *testing.T) {
	nested := t.TempDir()
	writeArtifactFile(t, nested, "index.js", "export default 1\n")
	writeArtifactFile(t, nested, "src/vendor/shim.js", "export default 2\n")
	if _, _, err := BuildArtifact(nested); err != nil {
		t.Fatalf("src/vendor should be allowed: %v", err)
	}

	var sawNested bool
	for _, n := range tarNames(t, mustBuild(t, nested)) {
		if n == "src/vendor/shim.js" {
			sawNested = true
		}
	}
	if !sawNested {
		t.Error("src/vendor/shim.js was dropped from the artifact")
	}

	root := t.TempDir()
	writeArtifactFile(t, root, "index.js", "export default 1\n")
	writeArtifactFile(t, root, "vendor/autoload.php", "<?php\n")
	for _, n := range tarNames(t, mustBuild(t, root)) {
		if n == "vendor/autoload.php" {
			t.Error("a root vendor/ reached the artifact")
		}
	}
}

func mustBuild(t *testing.T, dir string) []byte {
	t.Helper()
	tgz, _, err := BuildArtifact(dir)
	if err != nil {
		t.Fatalf("build %s: %v", dir, err)
	}
	return tgz
}

// tarNames lists the entry names inside a built artifact.
func tarNames(t *testing.T, tgz []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	var out []string
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, hdr.Name)
	}
	return out
}

func writeArtifactFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
