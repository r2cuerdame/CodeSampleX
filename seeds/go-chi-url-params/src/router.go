// Package sample builds a chi router with nested routes and middleware.
package sample

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// New returns a router. Two things trip people up: chi.URLParam reads from
// the REQUEST context, so a handler that was not reached through the router
// gets an empty string rather than an error; and middleware must be
// registered before the routes it should wrap, because Use() panics once a
// route has been added.
func New() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Traced", "yes")
			next.ServeHTTP(w, req)
		})
	})

	r.Route("/peers", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, req *http.Request) {
			w.Write([]byte("all peers"))
		})
		r.Get("/{peerID}/samples/{sampleID}", func(w http.ResponseWriter, req *http.Request) {
			w.Write([]byte(chi.URLParam(req, "peerID") + "/" + chi.URLParam(req, "sampleID")))
		})
	})
	return r
}
