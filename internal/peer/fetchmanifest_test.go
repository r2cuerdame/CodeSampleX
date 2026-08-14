package peer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A fetched sample used to be stored with an empty manifest, on the theory
// that shard sync would fill it in. The local row then existed with no
// packages, no goal and no environment — and the search engine prefers a
// local row over the shard entry that found it, so fetching a sample made
// that sample unfindable. get_sample poisoned the index for the very thing
// the agent had just asked about.
func TestFetchedManifestComesFromTheArtifact(t *testing.T) {
	want := domain.SampleManifest{
		SchemaVersion: 1,
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Symbols:       []string{"axios.post"},
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW", Goal: "post JSON with axios",
			Packages: []string{"pkg:npm/axios@1.12.0"},
		},
	}
	got := manifestFromArtifact(artifactWith(t, string(domain.MustCanonicalJSON(want))))

	var back domain.SampleManifest
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("stored manifest does not parse: %v", err)
	}
	if back.Case.Goal != want.Case.Goal {
		t.Errorf("goal = %q, want %q", back.Case.Goal, want.Case.Goal)
	}
	if len(back.Packages) != 1 || back.Packages[0] != want.Packages[0] {
		t.Errorf("packages = %v, want %v", back.Packages, want.Packages)
	}
}

// An artifact with no manifest keeps the old empty value: a row with no
// metadata is useless, but a row with the WRONG metadata would be worse.
func TestArtifactWithoutAManifestStaysEmpty(t *testing.T) {
	if got := manifestFromArtifact(artifactWith(t, "")); got != "{}" {
		t.Errorf("got %q, want {}", got)
	}
	if got := manifestFromArtifact([]byte("not a tarball")); got != "{}" {
		t.Errorf("got %q, want {} for junk input", got)
	}
}

// artifactWith builds a tar.gz carrying csx.json, or none when empty.
func artifactWith(t *testing.T, manifestJSON string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name, content string) {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: name, Size: int64(len(content)), Mode: 0o644,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	write("src/index.mjs", "export default 1\n")
	if manifestJSON != "" {
		write("csx.json", manifestJSON)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
