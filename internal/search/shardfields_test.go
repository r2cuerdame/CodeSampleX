package search

import (
	"encoding/json"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The server writes compatibility.ShardSample and this package reads
// shardSampleEntry. They are two declarations of one wire format, and a
// field that exists on only one side is not a compile error anywhere — it
// is a value that silently stops arriving.
//
// This test writes a shard sample the way the builder does and reads it the
// way the engine does, so a JSON tag that drifts fails here rather than in
// a client that quietly answers with less than the network knows.
func TestShardSampleFieldsSurviveTheRoundTrip(t *testing.T) {
	out := compatibility.ShardSample{
		SampleID: "sha256:aaaa",
		Goal:     "Set a per-phase timeout on an httpx request",
		Status:   "PUBLISHED",
		License:  "MIT-0",
		Packages: []string{"pkg:pypi/httpx@0.28.1"},
		Verifications: []compatibility.ShardVerification{{
			ResolvedPackages:  []string{"pkg:pypi/httpx@0.28.2"},
			Stages:            map[string]string{"resolve": "PASS", "contract": "PASS"},
			VerificationLevel: 3,
		}},
		Believed: "a timeout of 5 covers the whole request",
		Contract: []string{"connect, read, write and pool each get their own 5 seconds"},
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "pypi", Language: "python",
		},
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var in shardSampleEntry
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ field, got, want string }{
		{"sampleId", in.SampleID, out.SampleID},
		{"goal", in.Goal, out.Goal},
		{"status", in.Status, out.Status},
		{"license", in.License, out.License},
		{"believed", in.Believed, out.Believed},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q — the two sides of the shard format have drifted",
				c.field, c.got, c.want)
		}
	}
	if len(in.Packages) != 1 || in.Packages[0] != out.Packages[0] {
		t.Errorf("packages = %v, want %v", in.Packages, out.Packages)
	}
	if len(in.Verifications) != 1 || len(in.Verifications[0].ResolvedPackages) != 1 ||
		in.Verifications[0].ResolvedPackages[0] != out.Verifications[0].ResolvedPackages[0] ||
		in.Verifications[0].Stages["contract"] != "PASS" {
		t.Errorf("verifications = %+v, want %+v", in.Verifications, out.Verifications)
	}
	if len(in.Contract) != 1 || in.Contract[0] != out.Contract[0] {
		t.Errorf("contract = %v, want %v", in.Contract, out.Contract)
	}
	if in.Environment.Ecosystem != "pypi" {
		t.Errorf("environment did not survive: %+v", in.Environment)
	}
}
