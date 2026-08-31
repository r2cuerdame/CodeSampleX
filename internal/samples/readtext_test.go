package samples

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// tgzOf builds a minimal artifact holding the given files, in order.
func tgzOf(t *testing.T, files [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: f[0], Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg,
			Format: tar.FormatUSTAR,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f[1])); err != nil {
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

// A sample's readable files come back as text, in archive order.
func TestReadTextFilesReturnsSourceInOrder(t *testing.T) {
	got, err := ReadTextFiles(tgzOf(t, [][2]string{
		{"csx.json", `{"schemaVersion":1}`},
		{"main.go", "package main\n\nfunc main() {}\n"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2: %+v", len(got), got)
	}
	if got[0].Name != "csx.json" || got[1].Name != "main.go" {
		t.Errorf("order = %s, %s", got[0].Name, got[1].Name)
	}
	if !strings.Contains(got[1].Body, "func main()") {
		t.Errorf("main.go body = %q", got[1].Body)
	}
	if got[0].Truncated || got[1].Truncated {
		t.Error("a short file was reported truncated")
	}
}

// A binary file is left out entirely rather than rendered as replacement
// characters.
//
// A sample's fixtures can be binary, and a page of U+FFFD is not source
// anybody can read or copy — it is noise wearing the shape of an answer. The
// file list on the page still names it, which is where a reader learns it
// exists.
func TestABinaryFileIsOmittedRatherThanMangled(t *testing.T) {
	got, err := ReadTextFiles(tgzOf(t, [][2]string{
		{"fixture.bin", "\x00\x01\x02binary\xff\xfe"},
		{"readme.md", "# readable\n"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "readme.md" {
		t.Errorf("got %+v, want only readme.md", got)
	}
}

// A file longer than the display bound is cut, and says so — a reader must not
// copy half a file believing it is whole.
func TestALongFileIsCutAndSaysSo(t *testing.T) {
	long := strings.Repeat("x", MaxViewBytes+500)
	got, err := ReadTextFiles(tgzOf(t, [][2]string{{"big.txt", long}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d files", len(got))
	}
	if len(got[0].Body) != MaxViewBytes {
		t.Errorf("body is %d bytes, want the %d-byte bound", len(got[0].Body), MaxViewBytes)
	}
	if !got[0].Truncated {
		t.Error("a cut file did not say it was cut")
	}

	// Exactly at the bound is whole, not truncated. Off by one here would
	// label every full-size file a fragment.
	exact := strings.Repeat("y", MaxViewBytes)
	got, err = ReadTextFiles(tgzOf(t, [][2]string{{"exact.txt", exact}}))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Truncated {
		t.Error("a file exactly at the bound was reported truncated")
	}
}

// The safety posture is Unpack's, and it has to still be there: a name that
// escapes the tree is refused rather than read.
//
// Nothing writes to disk here, so traversal cannot overwrite a file — but a
// path like ../../etc/passwd rendered on a public page as if it were part of
// the sample is its own kind of false statement about what the sample
// contains.
func TestAnEscapingNameIsRefused(t *testing.T) {
	if _, err := ReadTextFiles(tgzOf(t, [][2]string{{"../escape.txt", "no"}})); err == nil {
		t.Error("a traversing entry name was accepted")
	}
	if _, err := ReadTextFiles(tgzOf(t, [][2]string{{"/absolute.txt", "no"}})); err == nil {
		t.Error("an absolute entry name was accepted")
	}
}

// An artifact past the compressed cap is refused before it is decompressed.
func TestAnOversizedArtifactIsRefused(t *testing.T) {
	if _, err := ReadTextFiles(make([]byte, MaxCompressedBytes+1)); err == nil {
		t.Error("an artifact past the compressed cap was accepted")
	}
}
