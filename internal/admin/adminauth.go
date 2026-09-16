package admin

import (
	"net/http"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// AdminAuth builds the same operator authentication the /admin dashboard's
// own handler enforces -- HTTP Basic against the "recuerdame" username and
// TokenSHA256's digest, or a Bearer operator API token resolved through
// tokens -- as middleware another package's operator-only route can wrap
// itself in, instead of building a second, independently maintained auth
// mechanism (CSX-454: GET /v1/ops/pool-metrics is the first caller).
//
// ok is false, and mw is nil, exactly when tokenSHA256 is not a valid
// 64-character hex SHA-256 digest -- the same gate Register applies before
// mounting /admin at all. A caller should not mount its route in that case
// either: an admin surface with no configured credential must be
// indistinguishable from an unknown path, not merely refuse every request
// that reaches it.
func AdminAuth(tokenSHA256 string, tokens serverstore.AdminTokenStore) (mw func(http.Handler) http.Handler, ok bool) {
	wantHash, ok := parseDigest(tokenSHA256)
	if !ok {
		return nil, false
	}
	h := &handler{wantHash: wantHash, adminTokens: tokens, now: time.Now}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			setPrivateHeaders(w.Header())
			if _, ok := h.requireAdmin(w, r); !ok {
				return
			}
			next.ServeHTTP(w, r)
		})
	}, true
}
