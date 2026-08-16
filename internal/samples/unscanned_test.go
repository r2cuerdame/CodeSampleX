package samples

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The publish gate refuses on leakage findings and tells the user "There is
// no override flag". That promise is only as good as the scan behind it,
// and the scan skipped every file over 2MB while an artifact may carry 8MB
// unpacked — silently, so `csx sample create` printed "Leakage findings: 0"
// and publish found nothing to refuse.
//
// A 3MB fixture with a credential in the middle of it went out.
func TestALargeFileIsNotPublishedUnscanned(t *testing.T) {
	dir := t.TempDir()
	// ~3MB of filler with a credential buried in it, as a captured API
	// response fixture would look.
	filler := strings.Repeat(`{"row":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`+"\n", 70000)
	body := filler + `{"aws_access_key_id": "AKIAIOSFODNN7EXAMPLE", "note": "prod"}` + "\n" + filler
	if len(body) < 2<<20 {
		t.Fatalf("fixture is %d bytes, must exceed the old 2MB cap", len(body))
	}
	write(t, dir, "fixture.json", body)
	write(t, dir, "main.py", "print('hello')\n")

	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("a 3MB file carrying an AWS key produced no findings at all")
	}
	for _, f := range findings {
		if f.File == "fixture.json" {
			return // either the key itself or an explicit UNSCANNED mark
		}
	}
	t.Errorf("nothing was reported about fixture.json: %+v", findings)
}

// A file the scanner cannot read is reported, not passed over. "We did not
// look" must never be delivered as "there is nothing there".
func TestAnUnreadableFileIsReportedRatherThanSkipped(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.py", "print('hello')\n")
	// A file larger than anything an artifact may carry.
	huge := filepath.Join(dir, "huge.txt")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxScanFileBytes + 1); err != nil {
		f.Close()
		t.Skipf("cannot create a sparse file here: %v", err)
	}
	f.Close()

	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var marked bool
	for _, fd := range findings {
		if fd.File == "huge.txt" && fd.Kind == KindUnscanned {
			marked = true
		}
	}
	if !marked {
		t.Errorf("a file too large to check was passed over silently: %+v", findings)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The host allowlist answers "is it normal for a sample to link here",
// which is a different question from "is there a secret in this string".
// A source file could therefore carry
// https://npm_token@registry.npmjs.org/ straight into a published sample,
// because that host is exactly the one a sample is expected to reference.
//
// The lockfile branch had checked this from the beginning. Source files —
// where a contributor actually types things — did not.
func TestACredentialInAnAllowedURLIsStillCaught(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "index.mjs", "const feed = \"https://npm_secrettoken123@registry.npmjs.org/\";\n")

	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.File == "index.mjs" && f.Kind == KindURL {
			return
		}
	}
	t.Errorf("a token embedded in an allowed host's URL was not reported: %+v", findings)
}

// A plain URL to an allowed host is still fine — that is what the allowlist
// is for, and flagging it would make the gate unusable.
func TestAPlainAllowedURLIsNotAFinding(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "index.mjs", "const url = \"https://registry.npmjs.org/axios\";\n")

	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Kind == KindURL {
			t.Errorf("an ordinary allowed URL was reported as a leak: %+v", f)
		}
	}
}

// "Anything that is not whitespace" also describes a regular expression.
// A sample about the regex crate contains r"(\N\{[^}]+})|([{}])", whose
// middle reads as two backslashes, a name, a backslash and then junk — and
// the publish gate refused it with no override flag, turning a contributor
// away for writing exactly the sample the network asked for.
func TestARegexLiteralIsNotAUNCPath(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "lib.rs", `        let re = Regex::new(r"(\\N\{[^}]+})|([{}])").unwrap();`+"\n")

	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Kind == KindAbsolutePath {
			t.Errorf("a regex literal was reported as a path: %+v", f)
		}
	}
}

// A real UNC path is still caught — that is what the rule is for.
func TestARealUNCPathIsStillCaught(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "config.js", "const share = \"\\\\fileserver\\acme-payroll\\out.csv\";\n")

	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Kind == KindAbsolutePath {
			return
		}
	}
	t.Errorf("a UNC share pointing at a company file server was not reported: %+v", findings)
}

// A URL inside a string literal carries the string's escapes with it:
// "API_URL=https://api.example.com\n" yields the host api.example.com\n.
// Removing the backslash gave api.example.comn — not an allowlisted host —
// so a sample whose URL pointed at example.com, the host reserved for
// exactly this, was refused at publish with no override.
//
// A backslash before a dot is a regex escape and comes out; a backslash
// before anything else ends the host.
func TestAStringEscapeDoesNotBreakAnAllowedHost(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "contract.mjs", `const env = "API_URL=https://api.example.com\n";`+"\n")

	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Kind == KindURL {
			t.Errorf("an allowed host was refused because of a string escape: %+v", f)
		}
	}
}

// The regex-escaped form still resolves, and a host that is NOT allowed is
// still caught when written that way.
func TestARegexEscapedHostIsStillResolved(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.rb", `assert_match %r{https://api\.example\.com/items}, url`+"\n")
	findings, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Kind == KindURL {
			t.Errorf("a regex-escaped allowed host was reported: %+v", f)
		}
	}

	dir2 := t.TempDir()
	write(t, dir2, "spec.rb", `assert_match %r{https://jenkins\.acme-corp\.internal/job}, url`+"\n")
	findings2, err := Scan(dir2, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var caught bool
	for _, f := range findings2 {
		if f.Kind == KindURL {
			caught = true
		}
	}
	if !caught {
		t.Errorf("an internal host written as a regex was not caught: %+v", findings2)
	}
}
