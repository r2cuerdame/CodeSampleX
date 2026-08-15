package python

import "testing"

// PEP 440 makes `==4.2.*` a PREFIX MATCH — a range, not a pin. Accepting it
// recorded observations under the literal version string "4.2.*", which no
// release has ever had, while the grader treated it as an exact version.
// `django==4.2.*` and `flask==3.*` are among the most common lines anyone
// writes in a requirements.txt.
func TestAWildcardPinIsNotAResolvedVersion(t *testing.T) {
	for _, line := range []string{
		"django==4.2.*",
		"flask == 3.*",
		"celery[redis]==5.*",
	} {
		if m := reqPinRe.FindStringSubmatch(line); m != nil {
			t.Errorf("%q accepted as a pin at version %q", line, m[2])
		}
	}
	// A real pin still resolves, including local and epoch forms.
	for _, tc := range []struct{ line, name, version string }{
		{"requests==2.31.0", "requests", "2.31.0"},
		{"urllib3[socks]==2.2.1", "urllib3", "2.2.1"},
		{"torch==2.4.0+cpu", "torch", "2.4.0+cpu"},
		{"pkg==1!2.0", "pkg", "1!2.0"},
	} {
		m := reqPinRe.FindStringSubmatch(tc.line)
		if m == nil {
			t.Errorf("%q was not read as a pin", tc.line)
			continue
		}
		if m[1] != tc.name || m[2] != tc.version {
			t.Errorf("%q -> %q@%q, want %q@%q", tc.line, m[1], m[2], tc.name, tc.version)
		}
	}
}
