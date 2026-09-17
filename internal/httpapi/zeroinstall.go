package httpapi

import "net/http"

// The zero-install surface (#318).
//
// A caller with no csx client reaches this server three ways: a script or
// a cloud agent's fetch tool, which need nothing beyond HTTPS; and a page in
// a browser, which needs the server to say that another origin may read the
// answer. The read routes below carry no credentials and serve public data,
// so the browser rule costs nothing to grant and its absence made the
// "use from a browser" claim false for every route on this list.
//
// It is granted route by route rather than on the whole mux: the worker,
// seeder and operator routes carry tokens, and a wildcard origin on those
// would be an invitation to send them from a page.

// openHeaders is the answer to "may a page on another origin read this".
// No credentials are ever accepted on these routes, so the wildcard is the
// honest value; a preflight is cached for a day because the answer never
// changes.
func openHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Accept")
	h.Set("Access-Control-Max-Age", "86400")
}

// open marks a route as reachable from any origin without credentials.
func (a *api) open(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		openHeaders(w)
		h(w, r)
	}
}

// preflight answers the OPTIONS request a browser sends before a POST with
// a JSON body. Registered only on the two zero-install POST routes: the v2
// search body and the execution footprint. Every other POST on this server
// expects a client, not a page.
func (a *api) preflight(w http.ResponseWriter, r *http.Request) {
	openHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}
