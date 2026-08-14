package sanitizer

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// An all-lowercase key with digits in it is a very ordinary shape for an API
// key, a session id or a base36 token, and the old rule — a digit AND
// (mixed case OR a symbol) — let every one of them through into a field
// named "sanitized" that is handed back to the caller.
func TestLowercaseTokensAreRedacted(t *testing.T) {
	secrets := []string{
		"sk9f2a8c1d4b7e3f6a2b8c5d",
		"ghs4k2m9x7p1q3r5t8w0y6z2",
		"abcdefghij0123456789abcd",
	}
	for _, secret := range secrets {
		got := Sanitize("Error: auth failed for token "+secret, domain.StageProjectTest, nil)
		if strings.Contains(got.Template, secret) {
			t.Errorf("secret %q survived into the sanitized template: %q", secret, got.Template)
		}
	}
}

// Widening the rule must not start eating the ordinary words that make an
// error message readable. The candidate pattern needs 20 characters, so the
// short technical words are never even considered.
func TestOrdinaryWordsSurvive(t *testing.T) {
	line := "sha256 checksum mismatch on node22 using utf8 and base64 encoding"
	got := Sanitize(line, domain.StageProjectTest, nil)
	for _, word := range []string{"sha256", "node22", "utf8", "base64", "checksum"} {
		if !strings.Contains(got.Template, word) {
			t.Errorf("%q was redacted out of %q", word, got.Template)
		}
	}
}
