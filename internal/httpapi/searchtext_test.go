package httpapi

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The relevance gate treats "shares no content word with the sample" as a
// miss. The searchable text carried raw purls, so pkg:npm/axios@1.12.0 put
// "pkg" and "npm" into EVERY sample's tokens — and any query containing
// either word opened the gate for every candidate in the store.
//
// A wrong HIT is worse than a MISS, and this is the server-side twin of the
// client bug where a package name's language prefix counted as naming it.
func TestAPurlDoesNotMakeEveryQueryRelevant(t *testing.T) {
	m := domain.SampleManifest{
		Case:     domain.Case{Goal: "post a JSON body", Kind: "HOW"},
		Packages: []string{"pkg:npm/axios@1.12.0"},
	}
	text := searchText(m)

	for _, noise := range []string{"pkg", "npm"} {
		if sharedContentTokens(noise+" chocolate cake recipe", text) > 0 {
			t.Errorf("%q counted as sharing content with an axios sample", noise)
		}
	}
	// Naming the package still counts — that is the whole point.
	if sharedContentTokens("axios post json", text) == 0 {
		t.Error("naming the package no longer counts as relevance")
	}
}
