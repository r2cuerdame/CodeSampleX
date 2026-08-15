package search

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
)

// A fingerprint is SHA256("v1|" + stage + "|" + code + "|" + template), so
// the same error hashes differently per stage. Everything that RECORDS an
// error knows its stage; the agent pasting a build log into
// search_known_solution does not, and the MCP hashed with an empty one.
//
// The result: no stored fingerprint could ever equal a searched one, on any
// install. weightErrorFingerprint is 0.60 -- the largest relevance term
// there is, larger than an exact version match -- and it had never once
// been applied. The rule that exempts "the failure the caller is looking
// for a fix to" from the known-failure demotion never fired either, so the
// sample that fixes the pasted error was the one demoted.
func TestASearchedErrorMatchesTheStageItWasRecordedAt(t *testing.T) {
	const errText = "TypeError: Cannot read properties of undefined (reading 'get')\n" +
		"    at Object.<anonymous> (/home/someone/app/index.js:12:9)"

	// What a recorder stores, knowing the stage it observed.
	recorded := sanitizer.Sanitize(errText, domain.StageProjectTest, nil)
	// What the MCP can compute, knowing only the text.
	searched := sanitizer.Sanitize(errText, "", nil)

	if recorded.Fingerprint == searched.Fingerprint {
		t.Fatal("fixture is wrong: the two fingerprints are supposed to differ by stage")
	}

	req := domain.SearchRequest{
		ErrorFingerprint:  searched.Fingerprint,
		ErrorFingerprints: searched.Fingerprints(),
	}
	syms := []shardSymbolEntry{{
		Failures: []shardFailure{{Fingerprint: recorded.Fingerprint, ErrorCode: recorded.Code}},
	}}
	fp, _ := errorHits(req, syms)
	if !fp {
		t.Error("the caller's own error did not match the failure it was recorded as")
	}
}

// A different error must still not match: the point is a fingerprint, not a
// wildcard.
func TestADifferentErrorStillDoesNotMatch(t *testing.T) {
	mine := sanitizer.Sanitize("ERR_REQUIRE_ESM: require() of ES Module /app/x.mjs", "", nil)
	theirs := sanitizer.Sanitize("TS2345: Argument of type 'string' is not assignable", domain.StageProjectTypecheck, nil)

	req := domain.SearchRequest{ErrorFingerprint: mine.Fingerprint, ErrorFingerprints: mine.Fingerprints()}
	syms := []shardSymbolEntry{{Failures: []shardFailure{{Fingerprint: theirs.Fingerprint}}}}
	if fp, _ := errorHits(req, syms); fp {
		t.Error("an unrelated error was reported as the same failure")
	}
}
