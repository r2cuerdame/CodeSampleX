package mcp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// The shard carries, per sample, the belief the sample's contract
// contradicts. explain_compatibility printed the observation counts first
// and the samples last as bare ids, so the one sentence that changes what a
// model writes next — the wrong answer it was about to reach for — sat below
// a wall of statistics or never appeared at all.
func TestExplainLeadsWithTheFinding(t *testing.T) {
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()

	const shard = `{
	  "generatedAt": "2026-08-20T00:00:00Z",
	  "packages": [{
	    "purl": "pkg:npm/axios@1.12.0",
	    "symbols": [{"family":"axios.post","stats":{"observationCount":41,"uniquePeerBuckets":3,"passRate":0.9}}],
	    "samples": [
	      {"sampleId":"sha256:aaa","goal":"post json","status":"CROSS_PASS",
	       "believed":"a timeout of five covers the whole request",
	       "symbols":["axios.post"],
	       "contractStages":{"contract":"PASS"}},
	      {"sampleId":"sha256:bbb","goal":"stream a download","status":"CROSS_PASS",
	       "believed":"responseType stream works in the browser too",
	       "symbols":["axios.get"],
	       "contractStages":{"contract":"PASS"}}
	    ]
	  }]
	}`
	if err := db.SaveShard(ctx, "npm/axios/1", "etag", shard); err != nil {
		t.Fatal(err)
	}

	text, _, err := explainFromShards(ctx, db, "pkg:npm/axios@1.12.0", "axios.post",
		domain.EnvironmentFingerprint{})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(text, "a timeout of five covers the whole request") {
		t.Fatal("the belief the contract contradicts is missing from the explanation")
	}
	// Before the statistics, because a model that reads only the top of the
	// reply still gets the sentence that changes what it writes.
	iFinding := strings.Index(text, "a timeout of five covers")
	iObs := strings.Index(text, "Observation evidence")
	if iObs >= 0 && iFinding > iObs {
		t.Errorf("the finding is below the observation counts: finding=%d observations=%d", iFinding, iObs)
	}
	// A symbol was asked for, so a finding about a different symbol of the
	// same package is not what was asked for.
	if strings.Contains(text, "responseType stream works in the browser") {
		t.Error("a finding for another symbol was reported under this one")
	}
}

// With no symbol named, the question is about the package, so every finding
// the package carries answers it.
func TestExplainReportsEveryFindingWhenNoSymbolIsNamed(t *testing.T) {
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	const shard = `{"generatedAt":"2026-08-20T00:00:00Z","packages":[{"purl":"pkg:npm/axios@1.12.0",
	  "samples":[
	    {"sampleId":"sha256:aaa","believed":"a timeout of five covers the whole request","symbols":["axios.post"],"status":"CROSS_PASS"},
	    {"sampleId":"sha256:bbb","believed":"responseType stream works in the browser too","symbols":["axios.get"],"status":"CROSS_PASS"}
	  ]}]}`
	if err := db.SaveShard(ctx, "npm/axios/1", "etag", shard); err != nil {
		t.Fatal(err)
	}
	text, _, err := explainFromShards(ctx, db, "pkg:npm/axios@1.12.0", "", domain.EnvironmentFingerprint{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a timeout of five covers", "responseType stream works"} {
		if !strings.Contains(text, want) {
			t.Errorf("package-level explanation is missing %q", want)
		}
	}
}
