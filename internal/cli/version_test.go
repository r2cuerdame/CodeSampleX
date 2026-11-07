package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"runtime"
	"sort"
	"testing"
)

// stubVersion pins what `csx version` reads so the assertions do not depend
// on how the test binary itself was built or stamped.
func stubVersion(t *testing.T, version, revision string) *bytes.Buffer {
	t.Helper()
	isolateHome(t)
	var out bytes.Buffer
	oldVersion, oldOutput, oldRevision := Version, versionOutput, versionRevision
	Version, versionOutput = version, &out
	versionRevision = func() string { return revision }
	t.Cleanup(func() { Version, versionOutput, versionRevision = oldVersion, oldOutput, oldRevision })
	return &out
}

// install.sh, install.ps1 and the updater's staged-binary self-test all
// compare this line byte for byte against "csx "+version. Adding --json must
// not move a single character of it.
func TestVersionTextLineIsTheInstallContract(t *testing.T) {
	out := stubVersion(t, "v0.2.23", "4ee22f0a1b2c3d4e5f60718293a4b5c6d7e8f901")
	if code := Main([]string{"version"}); code != 0 {
		t.Fatalf("csx version returned %d, want 0", code)
	}
	if got := out.String(); got != "csx v0.2.23\n" {
		t.Fatalf("csx version printed %q, want %q", got, "csx v0.2.23\n")
	}
}

func TestVersionJSONCarriesTheBuildMetadata(t *testing.T) {
	const revision = "4ee22f0a1b2c3d4e5f60718293a4b5c6d7e8f901"
	out := stubVersion(t, "v0.2.23", revision)
	if code := Main([]string{"version", "--json"}); code != 0 {
		t.Fatalf("csx version --json returned %d, want 0", code)
	}
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	dec.DisallowUnknownFields()
	var got versionReport
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("csx version --json is not the versionReport shape: %v\n%s", err, out.String())
	}
	want := versionReport{SchemaVersion: 1, Version: "v0.2.23", Revision: revision, OS: runtime.GOOS, Arch: runtime.GOARCH}
	if got != want {
		t.Fatalf("csx version --json = %+v, want %+v", got, want)
	}
	if dec.More() {
		t.Fatalf("csx version --json printed more than one document:\n%s", out.String())
	}
}

// A script reads the same key set from every build. An unstamped build (go
// test, a checkout without git) reports an empty revision rather than
// dropping the key or inventing a value.
func TestVersionJSONKeepsEveryKeyWhenTheBuildIsUnstamped(t *testing.T) {
	out := stubVersion(t, "dev (git)", "")
	if code := Main([]string{"version", "--json"}); code != 0 {
		t.Fatalf("csx version --json returned %d, want 0", code)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("csx version --json is not JSON: %v\n%s", err, out.String())
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"arch", "os", "revision", "schemaVersion", "version"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("csx version --json keys = %v, want %v", keys, want)
	}
	if doc["revision"] != "" {
		t.Fatalf("unstamped revision = %#v, want empty string", doc["revision"])
	}
	if doc["version"] != "dev (git)" {
		t.Fatalf("version = %#v, want the link-time stamp verbatim", doc["version"])
	}
}

// Every other --json surface rejects what it does not understand; version
// used to swallow its arguments, so a typo like --jsno printed text a parser
// then choked on.
func TestVersionRejectsUnknownArguments(t *testing.T) {
	for _, argv := range [][]string{{"version", "--jsno"}, {"version", "--json", "extra"}, {"version", "short"}} {
		out := stubVersion(t, "v0.2.23", "")
		if code := Main(argv); code != 2 {
			t.Errorf("csx %v returned %d, want 2", argv, code)
		}
		if out.Len() != 0 {
			t.Errorf("csx %v wrote %q to stdout on a usage error", argv, out.String())
		}
	}
}
