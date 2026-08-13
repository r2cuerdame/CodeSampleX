package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"

	sample "codesamplex.dev/sample/gochi/src"
)

func fail(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}

func get(h http.Handler, path string) (int, string, http.Header) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String(), rec.Header()
}

func main() {
	h := sample.New()

	if code, body, hdr := get(h, "/peers/ed25519-abc/samples/sha256-def"); code != 200 ||
		body != "ed25519-abc/sha256-def" {
		fail("nested route: code=%d body=%q", code, body)
	} else if hdr.Get("X-Traced") != "yes" {
		fail("middleware did not run: %v", hdr)
	}

	if code, body, _ := get(h, "/peers/"); code != 200 || body != "all peers" {
		fail("index route: code=%d body=%q", code, body)
	}

	if code, _, _ := get(h, "/nope"); code != http.StatusNotFound {
		fail("unmatched route code = %d, want 404", code)
	}

	fmt.Println("CONTRACT PASS: chi routed nested paths, read params and ran middleware")
}
