package httpapi

import (
	"bytes"
	"net/http"
	"testing"
)

func postRaw(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The far end of the loop had a numerator and no denominator. Adoptions
// arrived — 143 of them — and nothing said how many searches they were out
// of, because a hit stopped at the caller's local hits table while a miss
// uploaded a Wanted ask.
func TestASearchHitIsRecorded(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	body := `{"schemaVersion":1,"epoch":"2026-08-21","anonId":"abcdef0123456789",` +
		`"grade":"EXACT","resultsShown":3,"sampleId":"sha256:aaa","offerId":"offer-1"}`
	resp := postRaw(t, srv.URL+"/v1/search-hits", body)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d, want 204", resp.StatusCode)
	}
}

// A hit that showed nothing is not a hit. Accepting it would let a client
// inflate the denominator without ever surfacing an answer.
func TestAHitThatShowedNothingIsRefused(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	body := `{"schemaVersion":1,"epoch":"2026-08-21","anonId":"abcdef0123456789",` +
		`"grade":"EXACT","resultsShown":0}`
	resp := postRaw(t, srv.URL+"/v1/search-hits", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400 for a hit with nothing shown", resp.StatusCode)
	}
}

// The route is versioned, so it cannot silently drift from the shape the
// client sends.
func TestASearchHitNeedsItsSchemaVersion(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	body := `{"epoch":"2026-08-21","anonId":"abcdef0123456789","resultsShown":1}`
	resp := postRaw(t, srv.URL+"/v1/search-hits", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400 without a schemaVersion", resp.StatusCode)
	}
}
