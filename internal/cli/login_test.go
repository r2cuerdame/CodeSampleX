package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

func captureLoginIO(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	outBuf, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	oldOut, oldErr := loginStdout, loginStderr
	loginStdout, loginStderr = outBuf, errBuf
	t.Cleanup(func() { loginStdout, loginStderr = oldOut, oldErr })
	return outBuf, errBuf
}

func fastLoginPolling(t *testing.T) {
	t.Helper()
	oldInterval, oldTimeout := loginDefaultInterval, loginTimeout
	loginDefaultInterval = time.Millisecond
	loginTimeout = 5 * time.Second
	t.Cleanup(func() { loginDefaultInterval, loginTimeout = oldInterval, oldTimeout })
}

func TestLoginUsage(t *testing.T) {
	t.Setenv("CSX_HOME", t.TempDir())
	captureLoginIO(t)
	if code := Main([]string{"login"}); code != 2 {
		t.Fatalf("bare `csx login` exited %d, want 2", code)
	}
	if code := Main([]string{"login", "gitlab"}); code != 2 {
		t.Fatalf("`csx login gitlab` exited %d, want 2", code)
	}
}

func TestLoginGithubDeviceFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	fastLoginPolling(t)

	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/github/device", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deviceCode":"dc-1","userCode":"ABCD-1234",` +
			`"verificationUri":"https://github.com/login/device","interval":0}`))
	})
	mux.HandleFunc("POST /v1/auth/github/poll", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			DeviceCode string `json:"deviceCode"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.DeviceCode != "dc-1" {
			http.Error(w, "wrong device code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&polls, 1) < 3 {
			_, _ = w.Write([]byte(`{"pending":true}`)) // authorization pending
			return
		}
		_, _ = w.Write([]byte(`{"login":"alice","apiToken":"tok-once-42"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, errBuf := captureLoginIO(t)
	if code := Main([]string{"login", "github", "--server", srv.URL}); code != 0 {
		t.Fatalf("login exited %d\nstdout: %s\nstderr: %s", code, out, errBuf)
	}
	got := out.String()
	for _, want := range []string{"https://github.com/login/device", "ABCD-1234", "enter the code", "alice"} {
		if !strings.Contains(got, want) {
			t.Fatalf("login output missing %q:\n%s", want, got)
		}
	}
	if atomic.LoadInt32(&polls) < 3 {
		t.Fatalf("expected at least 3 polls, got %d", polls)
	}

	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GithubLogin != "alice" || cfg.APIToken != "tok-once-42" {
		t.Fatalf("config after login = login %q token %q", cfg.GithubLogin, cfg.APIToken)
	}
}

func TestLoginGithub501NotConfigured(t *testing.T) {
	t.Setenv("CSX_HOME", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/github/device", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "github identity not configured", http.StatusNotImplemented)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, errBuf := captureLoginIO(t)
	if code := Main([]string{"login", "github", "--server", srv.URL}); code == 0 {
		t.Fatalf("login against 501 server exited 0\nstdout: %s", out)
	}
	if !strings.Contains(errBuf.String(), "not configured") {
		t.Fatalf("expected clear not-configured message, got:\n%s", errBuf)
	}
}

func TestLoginGithubTimeout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	fastLoginPolling(t)
	oldTimeout := loginTimeout
	loginTimeout = 30 * time.Millisecond
	t.Cleanup(func() { loginTimeout = oldTimeout })

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/github/device", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deviceCode":"dc-2","userCode":"WXYZ-9999",` +
			`"verificationUri":"https://github.com/login/device","interval":0}`))
	})
	mux.HandleFunc("POST /v1/auth/github/poll", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pending":true}`)) // never authorizes
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, errBuf := captureLoginIO(t)
	if code := Main([]string{"login", "github", "--server", srv.URL}); code == 0 {
		t.Fatal("login exited 0 despite never being authorized")
	}
	if !strings.Contains(errBuf.String(), "timed out") {
		t.Fatalf("expected timeout message, got:\n%s", errBuf)
	}
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIToken != "" || cfg.GithubLogin != "" {
		t.Fatal("timed-out login must not save credentials")
	}
}
