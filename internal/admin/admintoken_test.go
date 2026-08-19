package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func tokenDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// A code-generation farm provisions itself: it holds one operator token and
// mints a worker session per instance over the API. There is no browser in
// that loop, so there is no password prompt and no Origin header -- and there
// does not need to be. The CSRF checks exist to stop a page the operator did
// not write from riding an ambient Basic credential; a bearer is never
// attached by a browser on its own, so they do not apply to it.
func TestAdminAPIAcceptsOperatorTokenWithoutBrowserCeremony(t *testing.T) {
	tokens := serverstore.NewFake()
	issued := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	if err := tokens.IssueAdminTokens(context.Background(), []serverstore.AdminTokenRow{{
		TokenHash: tokenDigest("csx_admin_farmkey"), TokenID: "farm", Label: "farm", IssuedAt: issued,
	}}); err != nil {
		t.Fatal(err)
	}
	mux := configuredMuxWithTokens(t, &fakeStore{}, tokens)

	body := `{"model":"agy","reasoning":"auto","count":3}`
	req := httptest.NewRequest(http.MethodPost, "/admin/api/authoring-sessions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer csx_admin_farmkey")
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.8:443"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"sessionId"`) {
		t.Errorf("no sessions came back: %s", rec.Body.String())
	}
}

// The token is the only credential that can be used without a browser, so a
// revoked or expired one must close that door completely.
func TestAdminAPIRefusesRevokedAndExpiredOperatorTokens(t *testing.T) {
	tokens := serverstore.NewFake()
	issued := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	if err := tokens.IssueAdminTokens(context.Background(), []serverstore.AdminTokenRow{
		{TokenHash: tokenDigest("csx_admin_revoked"), TokenID: "revoked", Label: "r", IssuedAt: issued},
		{TokenHash: tokenDigest("csx_admin_expired"), TokenID: "expired", Label: "e", IssuedAt: issued,
			ExpiresAt: issued.Add(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := tokens.RevokeAdminToken(context.Background(), "revoked", issued); err != nil || !ok {
		t.Fatal(err)
	}
	mux := configuredMuxWithTokens(t, &fakeStore{}, tokens)

	for _, raw := range []string{"csx_admin_revoked", "csx_admin_expired", "csx_admin_neverissued"} {
		req := httptest.NewRequest(http.MethodPost, "/admin/api/authoring-sessions",
			strings.NewReader(`{"model":"agy","reasoning":"auto","count":1}`))
		req.Header.Set("Authorization", "Bearer "+raw)
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "198.51.100.8:443"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 (body=%s)", raw, rec.Code, rec.Body.String())
		}
	}
}

// Basic auth from a browser still needs its CSRF proof. Adding a second way in
// must not weaken the first.
func TestAdminBrowserMutationStillNeedsCSRFProof(t *testing.T) {
	mux, secret := configuredMux(t, &fakeStore{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/authoring-sessions",
		strings.NewReader(`{"model":"agy","reasoning":"auto","count":1}`))
	req.SetBasicAuth("recuerdame", secret)
	req.Header.Set("Content-Type", "application/json")
	// No Origin, no X-CSX-CSRF.
	req.RemoteAddr = "198.51.100.8:443"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusCreated {
		t.Error("a browser mutation without CSRF proof was accepted")
	}
}

func issueTokens(t *testing.T, mux *http.ServeMux, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/admin-tokens", strings.NewReader(body))
	req.SetBasicAuth("recuerdame", secret)
	req.Header.Set("Origin", "https://codesamplex.dev")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSX-CSRF", "1")
	req.RemoteAddr = "198.51.100.8:443"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The operator asks for n credentials because a farm of n machines wants one
// each: shared, a single misbehaving instance cannot be cut off without
// cutting off all of them.
func TestAdminTokenIssuesOnePerMachineAndShowsThePlaintextOnce(t *testing.T) {
	tokens := serverstore.NewFake()
	mux, secret := configuredMuxFullWithTokens(t, &fakeStore{}, tokens)

	rec := issueTokens(t, mux, secret, `{"label":"farm","count":3,"unlimited":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var issued struct {
		Tokens []struct {
			TokenID string `json:"tokenId"`
			Label   string `json:"label"`
			Token   string `json:"token"`
			Expires string `json:"expiresAt"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if len(issued.Tokens) != 3 {
		t.Fatalf("got %d tokens, want 3", len(issued.Tokens))
	}
	seen := map[string]bool{}
	for i, tok := range issued.Tokens {
		if tok.Token == "" {
			t.Errorf("token %d came back without its plaintext", i)
		}
		if seen[tok.Token] {
			t.Errorf("token %d repeats an earlier secret", i)
		}
		seen[tok.Token] = true
		if tok.Expires != "" {
			t.Errorf("token %d was asked to be unlimited but expires at %q", i, tok.Expires)
		}
	}

	// The list is the operator's ongoing view, and it must never carry the
	// secret again.
	list := httptest.NewRequest(http.MethodGet, "/admin/api/admin-tokens", nil)
	list.SetBasicAuth("recuerdame", secret)
	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listed.Code, listed.Body.String())
	}
	for tok := range seen {
		if strings.Contains(listed.Body.String(), tok) {
			t.Fatal("the token list handed back a plaintext secret")
		}
	}
	if !strings.Contains(listed.Body.String(), `"tokenId"`) {
		t.Errorf("list carried no tokens: %s", listed.Body.String())
	}
}

// A permanent credential has to be asked for in so many words. An operator who
// simply forgets the duration must not be handed one by default.
func TestAdminTokenRequiresAnExplicitChoiceOfLifetime(t *testing.T) {
	tokens := serverstore.NewFake()
	mux, secret := configuredMuxFullWithTokens(t, &fakeStore{}, tokens)

	for _, body := range []string{
		`{"label":"farm","count":1}`,
		`{"label":"farm","count":1,"ttlDays":0}`,
		`{"label":"farm","count":1,"ttlDays":30,"unlimited":true}`,
	} {
		if rec := issueTokens(t, mux, secret, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, rec.Code)
		}
	}
	if rec := issueTokens(t, mux, secret, `{"label":"farm","count":1,"ttlDays":30}`); rec.Code != http.StatusCreated {
		t.Errorf("a bounded lifetime was refused: %d %s", rec.Code, rec.Body.String())
	}
}

// Revoking is the only way to stop an unlimited token.
func TestAdminTokenRevokeOverHTTP(t *testing.T) {
	tokens := serverstore.NewFake()
	mux, secret := configuredMuxFullWithTokens(t, &fakeStore{}, tokens)
	rec := issueTokens(t, mux, secret, `{"label":"farm","count":1,"unlimited":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("issue status = %d", rec.Code)
	}
	var issued struct {
		Tokens []struct {
			TokenID string `json:"tokenId"`
			Token   string `json:"token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil || len(issued.Tokens) != 1 {
		t.Fatalf("issued = %+v err=%v", issued, err)
	}

	del := httptest.NewRequest(http.MethodDelete, "/admin/api/admin-tokens/"+issued.Tokens[0].TokenID, strings.NewReader("{}"))
	del.SetBasicAuth("recuerdame", secret)
	del.Header.Set("Origin", "https://codesamplex.dev")
	del.Header.Set("Content-Type", "application/json")
	del.Header.Set("X-CSX-CSRF", "1")
	deleted := httptest.NewRecorder()
	mux.ServeHTTP(deleted, del)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d body=%s", deleted.Code, deleted.Body.String())
	}

	// And the revoked secret no longer opens the admin API.
	probe := httptest.NewRequest(http.MethodPost, "/admin/api/authoring-sessions",
		strings.NewReader(`{"model":"agy","reasoning":"auto","count":1}`))
	probe.Header.Set("Authorization", "Bearer "+issued.Tokens[0].Token)
	probe.Header.Set("Content-Type", "application/json")
	probe.RemoteAddr = "198.51.100.8:443"
	probed := httptest.NewRecorder()
	mux.ServeHTTP(probed, probe)
	if probed.Code != http.StatusUnauthorized {
		t.Errorf("a revoked token still authorized: %d", probed.Code)
	}
}
