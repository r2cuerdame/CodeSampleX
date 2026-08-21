package compatibility

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The whole published output of the classifier rested on this predicate.
// Production carries 303 clusters with a named cause and 2,246 with UNKNOWN,
// and every one of the 303 is ERR_MODULE_NOT_FOUND. There has never been an
// ERR_REQUIRE_ESM cluster.
//
// The distribution was reasoned about for ERR_REQUIRE_ESM, which names the
// module system: the code itself asserts CONFIGURATION and the numbers only
// shade it. ERR_MODULE_NOT_FOUND says something else — a specifier did not
// resolve — and its usual causes are a package moving or dropping a subpath
// in its exports map, or an incomplete install. Putting .72 on CONFIGURATION
// and .07 on LIBRARY_REGRESSION is backwards for it, and the page printed
// "your module configuration is wrong" over a library that had quietly moved
// an export.
func TestOnlyACodeThatNamesTheModuleSystemAssertsConfiguration(t *testing.T) {
	for _, code := range []string{"ERR_REQUIRE_ESM", "ERR_UNSUPPORTED_DIR_IMPORT"} {
		got := Hypotheses(code, nil, nil)
		if len(got) == 0 || got[0].Domain != domain.FailConfiguration {
			t.Errorf("%s = %+v, want the configuration reading it names", code, got)
		}
	}
	for _, code := range []string{"ERR_MODULE_NOT_FOUND", "ERR_ASSERTION", "ENOENT"} {
		got := Hypotheses(code, nil, nil)
		if len(got) != 1 || got[0].Domain != domain.FailUnknown {
			t.Errorf("%s = %+v, want UNKNOWN: nothing here names a cause", code, got)
		}
	}
}

// A substring match on "ESM" claimed any vendor code with those three letters
// anywhere in it.
func TestAVendorCodeIsNotAModuleSystemCode(t *testing.T) {
	for _, code := range []string{"PRISMA_CLIENT_ERROR", "ESMTP_TIMEOUT", "MY_ESM_THING"} {
		got := Hypotheses(code, nil, nil)
		if len(got) != 1 || got[0].Domain != domain.FailUnknown {
			t.Errorf("%s = %+v, want UNKNOWN: three letters are not a claim", code, got)
		}
	}
}
