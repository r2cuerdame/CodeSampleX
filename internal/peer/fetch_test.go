package peer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"sort"
	"strings"
	"testing"
)

// The content address proves what the bytes ARE; it says nothing about
// which file inside them describes them. Matching csx.json on its BASE NAME
// meant a nested copy — api/csx.json, a fixture, a decoy — became the
// sample's stored metadata whenever it sorted first, so the local row
// carried the wrong packages, goal, license and environment for an artifact
// whose bytes verified perfectly.
func TestOnlyTheRootManifestDescribesTheSample(t *testing.T) {
	real := `{"schemaVersion":1,"packages":["pkg:npm/axios@1.19.0"],` +
		`"case":{"schemaVersion":1,"kind":"HOW","goal":"the real one"},"license":"MIT-0"}`
	decoy := `{"schemaVersion":1,"packages":["pkg:npm/evil@9.9.9"],` +
		`"case":{"schemaVersion":1,"kind":"HOW","goal":"the decoy"},"license":"GPL-3.0"}`

	// api/ sorts before csx.json, so the decoy is reached first.
	tgz := tarGz(t, map[string]string{
		"api/csx.json": decoy,
		"csx.json":     real,
		"src/index.js": "export default 1\n",
	})
	got := manifestFromArtifact(tgz)
	if strings.Contains(got, "evil") || strings.Contains(got, "decoy") {
		t.Fatalf("a nested csx.json became the sample's metadata: %s", got)
	}
	if !strings.Contains(got, "the real one") {
		t.Errorf("the root manifest was not used: %s", got)
	}
}

// tarGz builds a gzipped tar with the given files, in map-key order after
// sorting, so a test can control which entry the reader reaches first.
func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		body := files[n]
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: n, Size: int64(len(body)), Mode: 0o644,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
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
