package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/anonymousclient"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestAnonymousIdentityIssuanceAndIPIndependence(t *testing.T) {
	f := serverstore.NewFake()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	a := &api{d: Deps{Store: f, Now: func() time.Time { return now }}}
	h := a.anonymous(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.WriteHeader(200)
	})
	request := func(id, ip string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://local/v1/stats", nil)
		r.RemoteAddr = ip
		r.Header.Set(anonymousclient.Header, id)
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	w := request("", "10.0.0.1:123")
	id := w.Header().Get(anonymousclient.Header)
	if len(id) != 64 {
		t.Fatal("no anonymous ID")
	}
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("credential response publicly cacheable")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("missing safe persistence cookie")
	}
	request(id, "10.0.0.2:999")                      // address changes do not change identity
	request(strings.Repeat("a", 64), "10.0.0.1:123") // shared address, different client
	r := httptest.NewRequest("GET", "http://local/v1/stats", nil)
	r.AddCookie(cookies[0])
	wc := httptest.NewRecorder()
	h(wc, r)
	if wc.Header().Get(anonymousclient.Header) != id {
		t.Fatal("cookie identity not reused")
	}
	m, err := f.AnonymousAnalytics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if m.DAU != 2 || m.NRU != 2 || m.MAU != 2 {
		t.Fatalf("IP changed identity: %+v", m)
	}
}

func TestAnonymousExclusionsAndUnsuccessfulRequests(t *testing.T) {
	for _, test := range []struct {
		path, method, auth, class string
		status                    int
	}{
		{"/v1/stats", "GET", "", "", 429}, {"/v1/stats", "GET", "", "", 500}, {"/v1/stats", "GET", "", "", 400},
		{"/v1/stats", "HEAD", "", "", 200}, {"/v1/stats", "GET", "Bearer token", "", 200},
		{"/v1/stats", "GET", "", "farm", 200}, {"/v1/stats", "GET", "", "operator", 200},
		{"/v1/verification/jobs", "GET", "", "", 200}, {"/v1/presence", "POST", "", "", 200},
		{"/healthz", "GET", "", "", 200}, {"/admin", "GET", "", "", 200}, {"/v1/auth/github/device", "POST", "", "", 200},
	} {
		t.Run(test.path+test.method+test.auth+test.class+http.StatusText(test.status), func(t *testing.T) {
			f := serverstore.NewFake()
			a := &api{d: Deps{Store: f}}
			h := a.anonymous(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status) })
			r := httptest.NewRequest(test.method, "http://local"+test.path, nil)
			r.Header.Set("Authorization", test.auth)
			r.Header.Set(anonymousclient.ClassHeader, test.class)
			h(httptest.NewRecorder(), r)
			m, _ := f.AnonymousAnalytics(context.Background(), time.Now())
			if m.TotalClients != 0 {
				t.Fatal("excluded request counted")
			}
		})
	}
}

type anonymousFailStore struct{ *serverstore.Fake }

func (anonymousFailStore) RecordAnonymousClient(context.Context, string, time.Time) error {
	return errors.New("unavailable")
}
func TestAnonymousWriteFailurePreservesResponse(t *testing.T) {
	a := &api{d: Deps{Store: anonymousFailStore{serverstore.NewFake()}}}
	w := httptest.NewRecorder()
	a.anonymous(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("original")) })(w, httptest.NewRequest("GET", "http://local/v1/stats", nil))
	if w.Code != 200 || w.Body.String() != "original" {
		t.Fatal("analytics broke API")
	}
}

func TestAnonymous304AndMalformedID(t *testing.T) {
	f := serverstore.NewFake()
	a := &api{d: Deps{Store: f}}
	h := a.anonymous(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(304) })
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://local/v1/stats", nil)
	r.Header.Set(anonymousclient.Header, "not-an-id")
	h(w, r)
	if len(w.Header().Get(anonymousclient.Header)) != 64 {
		t.Fatal("malformed ID accepted")
	}
	m, _ := f.AnonymousAnalytics(context.Background(), time.Now())
	if m.DAU != 1 {
		t.Fatal("304 activity omitted")
	}
}
