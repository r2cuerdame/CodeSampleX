package sanitizer

import "strings"

import "testing"

// The anomaly channel's prose fields are written by a language model that was
// told not to include a path. These are the cases where it did anyway.
func TestRedactStripsWhatMustNeverTravel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		gone []string
	}{
		{"windows path", `the build failed in C:\Users\ana\proj\src\index.ts`, []string{"ana", "proj", "index.ts"}},
		{"unix path", "cannot open /home/ana/secrets/app.key", []string{"ana", "app.key"}},
		{"url", "posted to https://internal.acme.corp/api/v1/keys", []string{"acme", "internal"}},
		{"email", "reported by ana@acme.corp", []string{"acme.corp"}},
		{"quoted literal", `password "hunter2-correct-horse" rejected`, []string{"hunter2"}},
		{"token run", "authorization: bearer Zq7bY2mK9pW3nT6vR8sL1xC5dH0gJe4A", []string{"Zq7bY2mK9pW3nT6vR8sL1xC5dH0gJe4A"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clean, redacted := Redact(tc.in)
			if !redacted {
				t.Fatalf("Redact reported nothing removed from %q", tc.in)
			}
			for _, secret := range tc.gone {
				if strings.Contains(clean, secret) {
					t.Fatalf("%q survived redaction: %q", secret, clean)
				}
			}
		})
	}
}

// Redaction must not be so eager that an ordinary public statement comes back
// mangled — that is how a channel stops being used.
func TestRedactLeavesAPublicSentenceAlone(t *testing.T) {
	in := "axios.post returns 413 on node 22 with musl, but the sample records PASS"
	clean, redacted := Redact(in)
	if redacted || clean != in {
		t.Fatalf("a public sentence was altered: %q", clean)
	}
}
