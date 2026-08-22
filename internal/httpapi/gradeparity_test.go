package httpapi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	localsearch "github.com/r2cuerdame/codesamplex/internal/search"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// The two graders must reach the same rung.
//
// The existing parity test compares Miss and the top sample id and stops
// there, so a grade divergence passes CI unnoticed — and two of them were
// living in it. A caller asking /v1/search gets the server's answer with no
// way to know the client would have said something else, and the server was
// the more endorsing side both times.
func gradeParity(t *testing.T, name string, sampleEnv domain.EnvironmentFingerprint,
	req domain.SearchRequest, goal, purl, symbol string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		targetID := "sha256:" + strings.Repeat("a", 64)
		manifest := parityManifest(goal, purl, symbol, sampleEnv)

		db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := localsearch.SeedSampleDoc(context.Background(), db, manifest, targetID, "CROSS_PASS"); err != nil {
			t.Fatal(err)
		}
		srv, store, _ := newTestServer(t, nil)
		saveSearchFixture(t, store, targetID, goal, purl, symbol, sampleEnv)

		req.SchemaVersion = 2
		local := (localsearch.Engine{DB: db}).Search(t.Context(), req)
		var server domain.SearchResponse
		postJSON(t, srv.URL+"/v2/search", req, &server)

		if local.Miss != server.Miss {
			t.Fatalf("miss parity local=%v server=%v", local.Miss, server.Miss)
		}
		if local.Miss {
			return
		}
		if len(local.Results) == 0 || len(server.Results) == 0 {
			t.Fatalf("no results local=%d server=%d", len(local.Results), len(server.Results))
		}
		lg, sg := local.Results[0].Grade, server.Results[0].Grade
		if lg != sg {
			t.Errorf("grade divergence: local=%s server=%s\n  local different=%v\n  server different=%v",
				lg, sg, local.Results[0].Different, server.Results[0].Different)
		}
	})
}

// A sample from another ecosystem, in an environment nobody declared. The
// client caps it at ADAPTATION_REQUIRED and names the mismatch; the server
// blanked the request's ecosystem before comparing, so the block never ran
// and it answered EXACT with an empty Different list — the strongest grade
// in the system, for a sample from the wrong ecosystem, on a public
// unauthenticated endpoint.
func TestGradeParityAcrossEcosystems(t *testing.T) {
	pypiSample := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "pypi", OS: "linux", Arch: "amd64",
		Runtime: "python", RuntimeVersion: "3.13", Language: "python", PackageManager: "pip",
	}.Normalize()
	npmCaller := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.0.0", Language: "javascript", PackageManager: "npm",
	}.Normalize()

	gradeParity(t, "inferred environment, foreign ecosystem", pypiSample,
		domain.SearchRequest{
			Query:                 "freeze time in a python test",
			Environment:           npmCaller,
			EnvironmentProvenance: domain.SearchProvenanceContext,
		},
		"freeze time in a python test", "pkg:pypi/freezegun@1.5.5", "freezegun.freeze_time")
}

// Same runtime, different MAJOR. The client refuses (REFERENCE_ONLY); the
// server called it an enumerable adaptation and told the caller to "verify on
// python 3.13" — as though running a Python 2.7 sample on 3.13 were a matter
// of checking.
func TestGradeParityOnARuntimeMajorDifference(t *testing.T) {
	old := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "pypi", OS: "linux", Arch: "amd64",
		Runtime: "python", RuntimeVersion: "2.7.18", Language: "python", PackageManager: "pip",
	}.Normalize()
	caller := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "pypi", OS: "linux", Arch: "amd64",
		Runtime: "python", RuntimeVersion: "3.13", Language: "python", PackageManager: "pip",
	}.Normalize()

	gradeParity(t, "runtime major difference", old,
		domain.SearchRequest{
			Query:                 "freeze time in a python test",
			Environment:           caller,
			EnvironmentProvenance: domain.SearchProvenanceExplicit,
		},
		"freeze time in a python test", "pkg:pypi/freezegun@1.5.5", "freezegun.freeze_time")
}

// A MINOR difference is an adaptation on both sides. This one already agreed
// and must keep agreeing.
func TestGradeParityOnARuntimeMinorDifference(t *testing.T) {
	sample := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "pypi", OS: "linux", Arch: "amd64",
		Runtime: "python", RuntimeVersion: "3.12.4", Language: "python", PackageManager: "pip",
	}.Normalize()
	caller := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "pypi", OS: "linux", Arch: "amd64",
		Runtime: "python", RuntimeVersion: "3.10.11", Language: "python", PackageManager: "pip",
	}.Normalize()

	gradeParity(t, "runtime minor difference", sample,
		domain.SearchRequest{
			Query:                 "freeze time in a python test",
			Environment:           caller,
			EnvironmentProvenance: domain.SearchProvenanceExplicit,
		},
		"freeze time in a python test", "pkg:pypi/freezegun@1.5.5", "freezegun.freeze_time")
}
