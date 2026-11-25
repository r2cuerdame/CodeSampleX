package sanitizer

import (
	"strings"
	"testing"
)

func TestPublicDiagnosticMessageRedactsAndIsIdempotent(t *testing.T) {
	cases := map[string]string{
		`C:\Users\someone\proj\main.go:12:2: "io/ioutil" has been deprecated since Go 1.19`: `<path> "io/ioutil" has been deprecated since Go 1.19`,
		"warning:   see https://example.com/x for details\n  next line":                     "warning: see <url> for details next line",
		"/home/u/app/src/a.ts(3,1): 'foo' is deprecated.":                                   "<path>: 'foo' is deprecated.",
	}
	for raw, want := range cases {
		got := PublicDiagnosticMessage(raw)
		if got != want {
			t.Errorf("PublicDiagnosticMessage(%q) = %q, want %q", raw, got, want)
		}
		if again := PublicDiagnosticMessage(got); again != got {
			t.Errorf("not idempotent: %q -> %q", got, again)
		}
	}
	long := PublicDiagnosticMessage(strings.Repeat("é", 400))
	if len(long) > 512 || PublicDiagnosticMessage(long) != long {
		t.Fatalf("cap broken: %d bytes", len(long))
	}
}

func TestPublicDiagnosticFile(t *testing.T) {
	for raw, want := range map[string]string{
		`src\main.go`:       "src/main.go",
		"./pkg/a.go":        "pkg/a.go",
		"/abs/main.go":      "",
		`C:\x\main.go`:      "",
		"../outside.go":     "",
		"":                  "",
		"a/../../escape.go": "",
	} {
		if got := PublicDiagnosticFile(raw); got != want {
			t.Errorf("PublicDiagnosticFile(%q) = %q, want %q", raw, got, want)
		}
	}
}
