package httpapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// handleShard implements GET /v1/shards/{ecosystem}/{package...}/{major}
// with If-None-Match / ETag revalidation. Shards are pre-materialized by the
// aggregation builder; this handler only serves bytes.
func (a *api) handleShard(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Accept-Encoding")
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
			writeShardStoreErr(w, err)
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
		writeShardStoreErr(w, err)
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
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, writeErr := io.WriteString(gz, shardJSON)
		closeErr := gz.Close()
		if writeErr != nil || closeErr != nil {
			return
		}
		return
	}
	_, _ = io.WriteString(w, shardJSON)
}

func acceptsGzip(header string) bool {
	wildcardQuality := "1"
	wildcard := false
	for _, value := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(value), ";")
		encoding := strings.TrimSpace(parts[0])
		if strings.EqualFold(encoding, "*") {
			wildcard = true
			wildcardQuality = encodingQuality(parts[1:])
			continue
		}
		if !strings.EqualFold(encoding, "gzip") {
			continue
		}
		return qualityNonzero(encodingQuality(parts[1:]))
	}
	return wildcard && qualityNonzero(wildcardQuality)
}

func encodingQuality(params []string) string {
	quality := "1"
	for _, param := range params {
		name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), "q") {
			quality = strings.TrimSpace(value)
		}
	}
	return quality
}

func qualityNonzero(quality string) bool {
	return quality != "0" && quality != "0.0" && quality != "0.00" && quality != "0.000"
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

func writeShardStoreErr(w http.ResponseWriter, err error) {
	if serverstore.IsPoolBusy(err) {
		w.Header().Set("Retry-After", "2")
		writeErr(w, http.StatusTooManyRequests, "shard service busy; retry shortly")
		return
	}
	writeStoreErr(w, err, http.StatusInternalServerError, "shard lookup failed")
}
