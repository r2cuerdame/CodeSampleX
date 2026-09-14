// Package anonymousclient attaches a server-scoped pseudonym to community API
// traffic. It never creates network traffic or changes the caller's consent.
package anonymousclient

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/identity"
)

const Header = "X-CSX-Anonymous-ID"
const ClassHeader = "X-CSX-Client-Class"

type Transport struct {
	Home string
	Base http.RoundTripper
}

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	r = r.Clone(r.Context())
	// Strip even pre-existing headers on redirects to registries or peers.
	r.Header.Del(Header)
	r.Header.Del(ClassHeader)
	cfg, err := config.Load(t.Home)
	if err == nil && cfg.Mode == config.ModeCommunity && r.Header.Get("Authorization") == "" {
		u, err := url.Parse(cfg.ServerURL)
		if err == nil && u.User == nil && (u.Scheme == "https" || u.Scheme == "http") &&
			strings.EqualFold(u.Scheme, r.URL.Scheme) && strings.EqualFold(u.Host, r.URL.Host) &&
			(strings.HasPrefix(r.URL.Path, strings.TrimRight(u.Path, "/")+"/v1/") || strings.HasPrefix(r.URL.Path, strings.TrimRight(u.Path, "/")+"/v2/")) {
			if id, err := identity.LoadOrCreate(t.Home); err == nil {
				r.Header.Set(Header, id.AnonymousClientID(strings.ToLower(u.Scheme+"://"+u.Host)+strings.TrimRight(u.Path, "/")))
				r.Header.Set(ClassHeader, cfg.EffectiveClientClass())
			}
		}
	}
	return base.RoundTrip(r)
}
