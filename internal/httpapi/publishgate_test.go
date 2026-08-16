package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func gateArtifact(t *testing.T) (domain.SampleManifest, string, []byte) {
	t.Helper()
	manifest := testManifest()
	art := buildArtifact(t, manifest, map[string]string{
		"src/index.mjs":     "import axios from 'axios';\nexport const post = axios.post;\n",
		"test/contract.mjs": "console.log('contract');\n",
	})
	return manifest, domain.SHA256Hex(art), art
}

// The shipped default refuses an anonymous upload.
func TestPublishRequiresASeeder(t *testing.T) {
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Cfg.Publishing = "" })
	manifest, id, art := gateArtifact(t)
	resp := postSample(t, srv.URL, manifest, id, art, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous upload = %d, want 403", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	// A refusal that only says "forbidden" teaches nothing. Every channel
	// that IS open has to be named where the caller meets the wall.
	for _, want := range []string{"evidence", "bug", "sample", "/contribute"} {
		if !strings.Contains(strings.ToLower(string(body)), want) {
			t.Errorf("the refusal does not mention %q: %s", want, body)
		}
	}
}

// A junk token is refused the same way, not accepted as "some identity".
func TestPublishRejectsAnUnknownToken(t *testing.T) {
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Cfg.Publishing = "" })
	manifest, id, art := gateArtifact(t)
	resp := postSample(t, srv.URL, manifest, id, art, "not-a-real-token")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown token = %d, want 403", resp.StatusCode)
	}
}

// A token that resolves to a seeder passes the gate. It may still be
// refused further in for its own reasons — what matters here is that the
// gate is not what stopped it.
func TestPublishAllowsASeeder(t *testing.T) {
	srv, store, _ := newTestServer(t, func(d *Deps) { d.Cfg.Publishing = "" })
	const token = "seeder-token-for-the-gate-test"
	if err := store.SaveIdentity(context.Background(), "millwright", 0, "", sha256Hex(token)); err != nil {
		t.Fatal(err)
	}
	manifest, id, art := gateArtifact(t)
	resp := postSample(t, srv.URL, manifest, id, art, token)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		t.Fatalf("a known seeder was refused by the gate: %s", body)
	}
}

// The dev escape hatch still works, or local development and the e2e runs
// have no way to publish at all.
func TestOpenPublishingAllowsAnonymous(t *testing.T) {
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Cfg.Publishing = "open" })
	manifest, id, art := gateArtifact(t)
	resp := postSample(t, srv.URL, manifest, id, art, "")
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		t.Fatal("open publishing must not refuse an anonymous upload")
	}
}
