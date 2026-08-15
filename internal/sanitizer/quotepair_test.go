package sanitizer

import (
	"strings"
	"testing"
)

// The Windows path rule swallowed its own boundary delimiter. When that
// delimiter was a quote, every remaining quote re-paired against the wrong
// partner, so the quoted-literal step deleted the text BETWEEN two secrets
// and left the second one standing.
//
// The surviving text is not merely stored: it is returned to the agent as
// "sanitized errors" — so it reaches the model provider — and it is hashed
// into the error fingerprint that gets uploaded.
func TestASecretAfterAWindowsPathDoesNotSurvive(t *testing.T) {
	raw := `Error: open "C:\Users\alice\app\config.json" failed; password "hunter2pass" rejected`
	got := Sanitize(raw, "", nil)
	if strings.Contains(got.Template, "hunter2pass") {
		t.Errorf("the password survived sanitizing:\n  %s", got.Template)
	}
	if strings.Contains(got.Template, "alice") {
		t.Errorf("the username survived sanitizing:\n  %s", got.Template)
	}
}

// The same shape with single quotes, and with the path first on the line.
func TestQuotePairingSurvivesEveryPathPosition(t *testing.T) {
	for _, raw := range []string{
		`open 'C:\Users\bob\x.json' failed; token 'ghp_secretvalue' rejected`,
		`C:\Users\carol\app\main.go:12: secret "swordfish" is invalid`,
		`cannot read "C:\Users\dave\.ssh\id_rsa": key "topsecret" unusable`,
	} {
		got := Sanitize(raw, "", nil)
		for _, leak := range []string{"bob", "carol", "dave", "swordfish", "topsecret", "ghp_secretvalue"} {
			if strings.Contains(got.Template, leak) {
				t.Errorf("%q survived in:\n  %s", leak, got.Template)
			}
		}
	}
}
