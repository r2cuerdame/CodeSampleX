package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestHealthz(t *testing.T) {
	mux := BuildMux(serverstore.ServerConfig{}, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want \"ok\"", body)
	}
}

func TestHealthzMethodAndUnknownPath(t *testing.T) {
	mux := BuildMux(serverstore.ServerConfig{}, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/healthz", "text/plain", nil)
	if err != nil {
		t.Fatalf("POST /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status = %d, want 405", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatalf("GET /nope: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /nope status = %d, want 404", resp.StatusCode)
	}
}

func TestRunRejectsUnknownSubcommand(t *testing.T) {
	if code := run([]string{"frobnicate"}, io.Discard, io.Discard); code != 2 {
		t.Fatalf("unknown subcommand exit = %d, want 2", code)
	}
	if code := run(nil, io.Discard, io.Discard); code != 2 {
		t.Fatalf("no subcommand exit = %d, want 2", code)
	}
}

func TestRunMigrateRequiresDSN(t *testing.T) {
	t.Setenv("CSX_DSN", "")
	if code := run([]string{"migrate"}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("migrate without CSX_DSN exit = %d, want 1", code)
	}
	t.Setenv("CSX_DSN", "")
	if code := run([]string{"serve"}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("serve without CSX_DSN exit = %d, want 1", code)
	}
}
