package httpapi

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// saveSearchable stores one sample the search can reach.
func saveSearchable(t *testing.T, store *serverstore.Fake, id string, m domain.SampleManifest) {
	t.Helper()
	if err := store.SaveSample(t.Context(), serverstore.SampleRow{
		SampleID: id, ManifestJSON: string(domain.MustCanonicalJSON(m)),
		Status: "PUBLISHED", License: "MIT-0", SizeBytes: 512, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
}

// The exemption for an error fingerprint only ever checked that the caller
// SENT one. So attaching any string at all switched the topic guard off and
// an off-topic question came back with an answer — the wrong HIT goal.md
// §3.8 calls worse than a miss.
func TestFingerprintDoesNotDisableTheTopicGuard(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	saveSearchable(t, store, "sha256:"+strings.Repeat("a1", 32), testManifest())

	ask := func(fingerprint string) int {
		var resp struct {
			Results []struct {
				SampleID string `json:"sampleId"`
			} `json:"results"`
		}
		body := map[string]any{
			"schemaVersion": 1,
			"query":         "how do i bake sourdough bread",
			"packages":      []string{"pkg:npm/axios@1.12.0"},
			"environment":   nodeEnv("esm"),
		}
		if fingerprint != "" {
			body["errorFingerprint"] = fingerprint
		}
		postJSON(t, srv.URL+"/v1/search", body, &resp)
		return len(resp.Results)
	}

	if n := ask(""); n != 0 {
		t.Fatalf("the off-topic control already hits without a fingerprint (%d); the test proves nothing", n)
	}
	if n := ask("deadbeefdeadbeefdeadbeefdeadbeef"); n != 0 {
		t.Errorf("an unrelated fingerprint bought %d result(s) for an off-topic question", n)
	}
}

// libc decides whether a package with a native module loads at all, and this
// project publishes findings that exist only because of it. Both sides
// declared it and nobody compared it, so a glibc caller was told EXACT by a
// sample verified only on musl.
func TestLibcMismatchIsReported(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	m := testManifest()
	m.Environment.Libc = "musl"
	saveSearchable(t, store, "sha256:"+strings.Repeat("b2", 32), m)

	env := nodeEnv("esm")
	env.Libc = "glibc"
	var resp struct {
		Results []struct {
			Match     string   `json:"match"`
			Different []string `json:"different"`
		} `json:"results"`
	}
	postJSON(t, srv.URL+"/v1/search", map[string]any{
		"schemaVersion": 1, "query": "post JSON with axios",
		"packages": []string{"pkg:npm/axios@1.12.0"}, "environment": env,
	}, &resp)

	if len(resp.Results) == 0 {
		t.Fatal("no result; the sample should still be offered, just not as EXACT")
	}
	r := resp.Results[0]
	if r.Match == "EXACT" {
		t.Error("a musl-only sample was graded EXACT for a glibc caller")
	}
	var mentioned bool
	for _, d := range r.Different {
		if strings.Contains(d, "libc") {
			mentioned = true
		}
	}
	if !mentioned {
		t.Errorf("libc difference not reported: %v", r.Different)
	}
}
