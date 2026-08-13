package domain

import (
	"testing"
)

func TestCanonicalJSONSortsKeys(t *testing.T) {
	got, err := CanonicalJSON(map[string]any{"b": 1, "a": map[string]any{"z": true, "y": "s"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"y":"s","z":true},"b":1}`
	if string(got) != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestCanonicalJSONPreservesNumbers(t *testing.T) {
	got, err := CanonicalJSON(map[string]any{"conf": 0.72, "n": int64(9007199254740993)})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"conf":0.72,"n":9007199254740993}`
	if string(got) != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestCanonicalJSONStructStable(t *testing.T) {
	e := EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64",
		Runtime: "node", RuntimeVersion: "22.18"}
	a := e.Hash()
	b := e.Hash()
	if a != b || len(a) != len("sha256:")+64 {
		t.Errorf("hash unstable or malformed: %q %q", a, b)
	}
	e2 := e
	e2.RuntimeVersion = "24.0"
	if e2.Hash() == a {
		t.Error("different fingerprints must hash differently")
	}
}

func TestCaseComputeIDIgnoresExistingID(t *testing.T) {
	c := Case{SchemaVersion: 1, Kind: "HOW", Goal: "Upload a multipart file using axios",
		Packages: []string{"pkg:npm/axios@1.12.0"}, Contract: []string{"assert body received"}}
	id1 := c.ComputeID()
	c.CaseID = id1
	if c.ComputeID() != id1 {
		t.Error("ComputeID must ignore the CaseID field itself")
	}
}

func TestReceiptSigningBytesExcludeSignature(t *testing.T) {
	r := VerificationReceipt{SchemaVersion: 1, SampleID: "sha256:ab", Stages: map[string]string{"resolve": "PASS"}}
	unsigned := string(r.SigningBytes())
	r.PeerSignature = "sig"
	if string(r.SigningBytes()) != unsigned {
		t.Error("SigningBytes must not include PeerSignature")
	}
}
