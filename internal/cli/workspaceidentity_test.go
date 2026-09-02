package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/samples"
)

// A workspace has to be able to say which sample it became.
//
// Reported from csx-farm-linux-1 (CodeSampleX-Farm#12), checked on the node
// rather than assumed: a csx-sample-* workspace holds PROMPT.md, csx.json,
// spec.json and the sample source, and carries no sample id, no submission
// record and no server ack. So nothing on that machine can answer "has this
// been durably ingested?" for a given directory, and `csx sample list` keys
// on an id the directory does not contain.
//
// The farm's collector had to fall back to a preserved-copy gate -- "is there
// a second copy of this on this machine" -- which is not the same question as
// "did the server take it". Measured there over 214 expired trees: 29 carried
// sample source and 9 of those existed nowhere else on the node. An age-only
// collector would have destroyed exactly those 9.
//
// Writing the id at create is the smaller of the two fixes the issue offered
// and makes the directory self-describing from the moment it becomes a
// sample, which anything reasoning about a tree after the fact also needs.
func TestSampleCreateRecordsWhichSampleTheWorkspaceBecame(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	dir := sampleFixtureDir(t, nil)

	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "create", dir}); code != 0 {
		t.Fatalf("sample create exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}

	raw, err := os.ReadFile(filepath.Join(dir, workspaceIdentityFile))
	if err != nil {
		t.Fatalf("the workspace does not say which sample it became: %v", err)
	}
	var got struct {
		SchemaVersion int    `json:"schemaVersion"`
		SampleID      string `json:"sampleId"`
		CreatedAt     string `json:"createdAt"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s is not JSON: %v", workspaceIdentityFile, err)
	}
	if !strings.HasPrefix(got.SampleID, "sha256:") {
		t.Errorf("sampleId = %q, want the content-addressed id", got.SampleID)
	}
	if got.SchemaVersion != 1 || got.CreatedAt == "" {
		t.Errorf("incomplete identity: %+v", got)
	}

	// And it names the sample the store actually holds, or it answers the
	// wrong question convincingly.
	db := openLocalDB(t, home)
	defer db.Close()
	rows, err := db.ListSamples(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SampleID != got.SampleID {
		t.Errorf("workspace names %q; the store holds %d sample(s)", got.SampleID, len(rows))
	}
}

// Creating again from the same directory must leave one current answer, not
// two contradictory ones. A tree that names two samples is worse than a tree
// that names none: a collector would believe the stale one.
func TestRecreatingAWorkspaceLeavesOneAnswer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	dir := sampleFixtureDir(t, nil)

	for range 2 {
		out, errBuf := captureSampleIO(t, "")
		if code := Main([]string{"sample", "create", dir}); code != 0 {
			t.Fatalf("sample create exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, workspaceIdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "sampleId"); n != 1 {
		t.Errorf("the workspace names %d sample ids after two creates", n)
	}
}

// The identity file must not travel inside the artifact.
//
// It describes the local directory, not the sample. A sample whose bytes
// change because of where it was authored is not content-addressed any more,
// and two nodes writing the same sources would disagree about its id.
//
// The check has to create twice in the SAME directory: the second create sees
// the file the first one wrote. Creating in two fresh directories proves
// nothing, because neither ever contains the file -- which is what an earlier
// version of this test did.
func TestTheIdentityFileIsNotPartOfTheSample(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	dir := sampleFixtureDir(t, nil)

	create := func() string {
		t.Helper()
		out, errBuf := captureSampleIO(t, "")
		if code := Main([]string{"sample", "create", dir}); code != 0 {
			t.Fatalf("sample create exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
		}
		raw, err := os.ReadFile(filepath.Join(dir, workspaceIdentityFile))
		if err != nil {
			t.Fatal(err)
		}
		var got workspaceIdentity
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		return got.SampleID
	}

	first := create()
	if _, err := os.Stat(filepath.Join(dir, workspaceIdentityFile)); err != nil {
		t.Fatalf("the first create wrote no identity: %v", err)
	}
	second := create() // this one sees the file the first one left

	if first != second {
		t.Errorf("the same sources produced %s and then %s; the identity file is inside the artifact",
			first, second)
	}
}

// A create killed mid-write must not move the next create's sample id.
//
// The identity is written through a temporary file in the same directory,
// because rename is the only way to publish it whole. That file is normally
// gone within two syscalls, but a process killed in that window leaves one
// in the directory the NEXT create packs -- and a sample id that depends on
// whether an earlier run was interrupted is not content-addressed.
func TestAStrayTempFileDoesNotMoveTheSampleID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	dir := sampleFixtureDir(t, nil)

	out, errBuf := captureSampleIO(t, "")
	if code := Main([]string{"sample", "create", dir}); code != 0 {
		t.Fatalf("sample create exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	clean, err := os.ReadFile(filepath.Join(dir, workspaceIdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	var before workspaceIdentity
	if err := json.Unmarshal(clean, &before); err != nil {
		t.Fatal(err)
	}

	// What an interrupted write leaves behind.
	stray := filepath.Join(dir, samples.WorkspaceTempPrefix+"3141592653")
	if err := os.WriteFile(stray, []byte("{\"sampleId\":\"half\""), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errBuf = captureSampleIO(t, "")
	if code := Main([]string{"sample", "create", dir}); code != 0 {
		t.Fatalf("sample create exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	raw, err := os.ReadFile(filepath.Join(dir, workspaceIdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	var after workspaceIdentity
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	if before.SampleID != after.SampleID {
		t.Errorf("a leftover %s moved the id from %s to %s",
			samples.WorkspaceTempPrefix+"*", before.SampleID, after.SampleID)
	}
}
