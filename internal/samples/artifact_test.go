package samples

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"fmt"
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
		"node_modules/x/index.js",
		".git/config",
		"venv/lib/a.py",
		".venv/lib/a.py",
		"target/debug/a.txt",
		"dist/bundle.js",
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
