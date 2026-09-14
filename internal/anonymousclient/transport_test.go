package anonymousclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/identity"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAnonymousTransportPersistenceScopeAndConsent(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = "https://api.example"
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	var got string
	transport := Transport{Home: home, Base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Get(Header)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	req := httptest.NewRequest("GET", "https://api.example/v1/stats", nil)
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	first := got
	if len(first) != 64 {
		t.Fatalf("id length %d", len(first))
	}
	if req.Header.Get(Header) != "" {
		t.Fatal("mutated caller request")
	}
	id, err := identity.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	if first != id.AnonymousClientID("https://api.example") {
		t.Fatal("not persisted identity")
	}
	if first == id.AnonymousClientID("https://other.example") {
		t.Fatal("cross-server link")
	}
	for _, target := range []string{"https://api.example.evil/v1/stats", "http://api.example/v1/stats", "https://api.example/admin", "https://peer.example/v1/stats"} {
		r := httptest.NewRequest("GET", target, nil)
		r.Header.Set(Header, first)
		r.Header.Set(ClassHeader, "ordinary")
		transport.RoundTrip(r)
		if got != "" {
			t.Fatalf("leaked ID to %s", target)
		}
	}
	req.Header.Set("Authorization", "Bearer test")
	transport.RoundTrip(req)
	if got != "" {
		t.Fatal("identified auth request as anonymous")
	}
	req.Header.Del("Authorization")
	for _, mode := range []string{config.ModeLocalOnly, config.ModeUninitialized} {
		cfg.Mode = mode
		cfg.Save(home)
		transport.RoundTrip(req)
		if got != "" {
			t.Fatal("attached ID without community mode")
		}
	}
	cfg.Mode = config.ModeCommunity
	cfg.Save(home)
	transport.RoundTrip(req)
	if got != first {
		t.Fatal("identity changed between config reloads")
	}
}

func TestAnonymousTransportRedirectStripsIdentifier(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = r.Header.Get(Header) != ""; w.WriteHeader(200) }))
	defer other.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Get(Header)) != 64 {
			t.Error("missing source identity")
		}
		http.Redirect(w, r, other.URL+"/v1/stats", 302)
	}))
	defer source.Close()
	home := t.TempDir()
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = source.URL
	cfg.Save(home)
	c := http.Client{Transport: Transport{Home: home}}
	res, err := c.Get(source.URL + "/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if leaked {
		t.Fatal("redirect leaked ID")
	}
}
