package samples

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scanOne writes one file into a scratch directory and returns the findings.
func scanOne(t *testing.T, name, body string) []Finding {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// An escaped CRLF is not a UNC share. This refused a finished sample about
// mime.getType and filenames containing line terminators, and it would have
// refused every sample about CSV quoting, HTTP framing or any wire protocol
// — the places where the traps are densest.
func TestEscapedNewlinesAreNotUNCShares(t *testing.T) {
	for _, body := range []string{
		`{"contract":["assert getType handles CRLF (\\r\\n) in filenames"]}`,
		`{"contract":["splits on (\\n\\t)"]}`,
		`const CRLF = "\\r\\n";`,
	} {
		for _, f := range scanOne(t, "csx.json", body) {
			if f.Kind == KindAbsolutePath {
				t.Errorf("%s flagged as a path in %q — nobody names a file server \"r\"",
					f.Excerpt, body)
			}
		}
	}
}

// The guard it is carved out of must not move: a real share still leaks the
// name of somebody's file server, and that is the thing this check is for.
func TestRealUNCSharesAreStillFound(t *testing.T) {
	for _, body := range []string{
		`const p = "\\\\fileserver\\builds\\out.dll";`,
		`path = '\\nas01\\data'`,
	} {
		var found bool
		for _, f := range scanOne(t, "main.js", body) {
			if f.Kind == KindAbsolutePath {
				found = true
			}
		}
		if !found {
			t.Errorf("no path finding for %q — a real share must still be caught", body)
		}
	}
}

// The library under test documents its own error messages, and a sample
// that asserts on one has to contain the URL the library prints.
func TestProjectDocumentationURLsAreAllowed(t *testing.T) {
	for _, raw := range []string{
		"https://reactjs.org/docs/error-decoder.html?invariant=299",
		"https://react.dev/errors/299",
		"https://opentelemetry.io/schemas/1.26.0",
	} {
		if !urlAllowed(raw, nil) {
			t.Errorf("%q was refused; it is the library's own published URL, "+
				"and a sample asserting on the error that prints it cannot omit it", raw)
		}
	}
}

// The allowlist is a list of PUBLIC project hosts, not a hole. A host that
// could be somebody's internal one is still refused.
func TestAllowlistDidNotBecomeAHole(t *testing.T) {
	for _, raw := range []string{
		"https://git.acme-corp.com/internal/repo",
		"https://reactjs.org.acme-corp.com/x",
	} {
		if urlAllowed(raw, nil) {
			t.Errorf("%q was allowed", raw)
		}
	}
	if strings.Contains(strings.Join(allowedURLHosts, " "), " ") == false {
		t.Skip("allowlist shape changed")
	}
}
