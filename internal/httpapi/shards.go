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

	etag, shardJSON, ok, err := a.d.Store.GetShard(r.Context(), key)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "shard lookup failed")
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
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	if header == "*" {
		return true
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
