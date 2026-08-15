package verifier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/samples"
)

// junkSource answers every fetch with a body no verifier should accept.
type junkSource struct{ body []byte }

func (j junkSource) Fetch(ctx context.Context, id string) ([]byte, string, error) {
	return j.body, "peer", nil
}

// "A broken peer path can never stall verification" — but an oversized body
// from a peer returned an error instead of falling through, so any peer on
// the network could block cross verification of any sample by answering
// with junk. Verification is the network's whole basis for trust, and this
// handed a stranger a switch to turn it off, one sample at a time.
func TestAJunkPeerResponseFallsBackToTheServer(t *testing.T) {
	const content = "the real artifact bytes"
	want := domain.SHA256Hex([]byte(content))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"oversized", []byte(strings.Repeat("x", samples.MaxCompressedBytes+1))},
		{"wrong hash", []byte("not the artifact")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cv := &CrossVerifier{ServerURL: srv.URL, HTTP: srv.Client(), Source: junkSource{tc.body}}
			got, err := cv.DownloadArtifact(context.Background(), want)
			if err != nil {
				t.Fatalf("a %s peer body stalled verification: %v", tc.name, err)
			}
			if string(got) != content {
				t.Errorf("got %q, want the server's bytes", got)
			}
		})
	}
}
