package httpapi

import (
	"io"
	"net/http"
	"net/url"
	"strings"
)

// handleShard implements GET /v1/shards/{ecosystem}/{package...}/{major}
// with If-None-Match / ETag revalidation. Shards are pre-materialized by the
// aggregation builder; this handler only serves bytes.
func (a *api) handleShard(w http.ResponseWriter, r *http.Request) {
	ecosystem := r.PathValue("ecosystem")
	rest := r.PathValue("rest")
	segs := strings.Split(rest, "/")
	if len(segs) < 2 {
		writeErr(w, http.StatusBadRequest, "path must be /v1/shards/{ecosystem}/{package}/{major}")
		return
	}
	major := segs[len(segs)-1]
	pkgName := strings.Join(segs[:len(segs)-1], "/")
	if un, err := url.PathUnescape(pkgName); err == nil {
		pkgName = un
	}
	key := ecosystem + "/" + pkgName + "/" + major

	// A revalidation is answered from the ETag alone. The 304 refund in the
	// rate limiter exists because a revalidation "did almost no work" — which
	// is only true if the shard document is never loaded on that path. In
	// production 2,606 of 4,074 shard requests were revalidations, so this is
	// the common case, not an optimization of a rare one.
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		etag, ok, err := a.d.Store.GetShardEtag(r.Context(), key)
		if err != nil {
			writeStoreErr(w, err, http.StatusInternalServerError, "shard lookup failed")
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "no shard for "+key)
			return
		}
		if etagMatches(inm, etag) {
			w.Header().Set("ETag", `"`+etag+`"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	etag, shardJSON, ok, err := a.d.Store.GetShard(r.Context(), key)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "shard lookup failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no shard for "+key)
		return
	}

	quoted := `"` + etag + `"`
	w.Header().Set("ETag", quoted)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(w, shardJSON)
}

// etagMatches implements If-None-Match comparison, tolerating quoted,
// unquoted, weak (W/) and multi-valued forms.
//
// "*" is deliberately NOT a match. RFC 7232 lets it match any current
// representation, but a real cache revalidates with the ETag it holds; *
// asserts nothing about held state, and honouring it paired with the
// limiter's 304 refund handed out unlimited full-cost reads that never
// depleted the budget. A client sending * gets the shard and is charged.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		v := strings.TrimSpace(part)
		v = strings.TrimPrefix(v, "W/")
		v = strings.Trim(v, `"`)
		if v == etag {
			return true
		}
	}
	return false
}
