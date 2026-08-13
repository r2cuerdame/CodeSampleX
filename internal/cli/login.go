package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

// Test seams (same unexported-var pattern as sample.go).
var (
	loginStdout io.Writer = os.Stdout
	loginStderr io.Writer = os.Stderr

	loginHTTP = &http.Client{Timeout: 30 * time.Second}
	// loginTimeout bounds the whole device flow (plan: 10min).
	loginTimeout = 10 * time.Minute
	// loginDefaultInterval is used when the server sends no poll interval.
	loginDefaultInterval = 5 * time.Second
)

func init() {
	Register(Command{
		Name:    "login",
		Summary: "log in with GitHub to publish samples under your name",
		Run:     loginMain,
	})
}

func loginMain(ctx context.Context, args []string) int {
	if len(args) < 1 || args[0] != "github" {
		fmt.Fprintln(loginStderr, "usage: csx login github [--server URL]")
		return 2
	}
	fs := flag.NewFlagSet("login github", flag.ContinueOnError)
	fs.SetOutput(loginStderr)
	server := fs.String("server", "", "server URL (default: config serverUrl)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(loginStderr, "csx login: %v\n", err)
		return 1
	}
	if err := config.EnsureHome(home); err != nil {
		fmt.Fprintf(loginStderr, "csx login: %v\n", err)
		return 1
	}
	cfg, err := config.Load(home)
	if err != nil {
		fmt.Fprintf(loginStderr, "csx login: %v\n", err)
		return 1
	}
	serverURL := strings.TrimRight(*server, "/")
	if serverURL == "" {
		serverURL = strings.TrimRight(cfg.ServerURL, "/")
	}

	device, code := loginPost(ctx, serverURL+"/v1/auth/github/device", []byte("{}"))
	if code == http.StatusNotImplemented {
		fmt.Fprintln(loginStderr, "csx login: GitHub identity is not configured on this server (HTTP 501).")
		fmt.Fprintln(loginStderr, "The server operator has not set CSX_GITHUB_CLIENT_ID/SECRET; anonymous")
		fmt.Fprintln(loginStderr, "publishing still works: csx sample publish <id> --anonymous")
		return 1
	}
	if code < 200 || code >= 300 {
		fmt.Fprintf(loginStderr, "csx login: device authorization failed: HTTP %d\n", code)
		return 1
	}
	var dev struct {
		DeviceCode      string `json:"deviceCode"`
		UserCode        string `json:"userCode"`
		VerificationURI string `json:"verificationUri"`
		Interval        int    `json:"interval"`
	}
	if err := json.Unmarshal(device, &dev); err != nil || dev.DeviceCode == "" {
		fmt.Fprintf(loginStderr, "csx login: bad device response: %v\n", err)
		return 1
	}

	fmt.Fprintf(loginStdout, "Open %s and enter the code: %s\n", dev.VerificationURI, dev.UserCode)
	fmt.Fprintln(loginStdout, "Waiting for authorization...")

	delay := time.Duration(dev.Interval) * time.Second
	if delay <= 0 {
		delay = loginDefaultInterval
	}
	deadline := time.Now().Add(loginTimeout)
	pollBody, _ := json.Marshal(map[string]string{"deviceCode": dev.DeviceCode})

	for {
		raw, code := loginPost(ctx, serverURL+"/v1/auth/github/poll", pollBody)
		if code == http.StatusNotImplemented {
			fmt.Fprintln(loginStderr, "csx login: GitHub identity is not configured on this server (HTTP 501).")
			return 1
		}
		if code >= 200 && code < 300 {
			var res struct {
				Login    string `json:"login"`
				APIToken string `json:"apiToken"`
			}
			if err := json.Unmarshal(raw, &res); err == nil && res.Login != "" && res.APIToken != "" {
				cfg.GithubLogin = res.Login
				cfg.APIToken = res.APIToken
				if err := cfg.Save(home); err != nil {
					fmt.Fprintf(loginStderr, "csx login: save config: %v\n", err)
					return 1
				}
				fmt.Fprintf(loginStdout, "Logged in as %s. API token saved to config.json (shown only once by the server).\n", res.Login)
				return 0
			}
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(loginStderr, "csx login: timed out waiting for GitHub authorization (10 minutes)")
			return 1
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(loginStderr, "csx login: %v\n", ctx.Err())
			return 1
		case <-time.After(delay):
		}
	}
}

// loginPost posts a JSON body and returns the response body + status code.
// Transport errors report status 0.
func loginPost(ctx context.Context, url string, body []byte) ([]byte, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := loginHTTP.Do(req)
	if err != nil {
		return nil, 0
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return raw, resp.StatusCode
}
