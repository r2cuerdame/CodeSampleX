package serverstore

import "testing"

// errorCode is the one free-ish string an anonymous reporter supplies that
// now travels back out on every relayed miss. It is safe only while it stays
// a compact machine code: a client that started putting fragments of real
// error text there would turn it into a quiet exfiltration channel bound to a
// stable anonymous id, and paths, hostnames and secrets live in that text.
func TestErrorCodeCannotCarryLogText(t *testing.T) {
	for _, leak := range []string{
		`C:\Users\alice\project\src\index.ts(4,10): error TS2345`,
		"/home/alice/acme-internal/node_modules/foo",
		"cannot find module 'foo' imported from bar",
		"connect ECONNREFUSED 10.0.3.14:5432",
		"failed at registry.acme-corp.internal",
		"ERR_X\nSECRET=hunter2",
	} {
		if err := validErrorCode(leak); err == nil {
			t.Errorf("errorCode accepted log text: %q", leak)
		}
	}

	// Real codes must survive. These are all present in this repo's own
	// contract-verified seeds, and the longest is 37 characters — a 32-byte
	// cap would have silently discarded the most specific ones.
	for _, code := range []string{
		"TS2345", "E0308", "ERR_REQUIRE_ESM", "MODULE_NOT_FOUND",
		"ERR_JWS_SIGNATURE_VERIFICATION_FAILED",
		"ERR_UNSUPPORTED_DATA_PAYLOAD_LENGTH",
		"java.lang.NoSuchMethodError", "C2039", "",
	} {
		if err := validErrorCode(code); err != nil {
			t.Errorf("errorCode rejected a real code %q: %v", code, err)
		}
	}
}
