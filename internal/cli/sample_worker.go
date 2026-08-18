package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const sampleWorkerResponseLimit = 32 << 10

var (
	sampleWorkerStdout io.Writer = os.Stdout
	sampleWorkerStderr io.Writer = os.Stderr
	sampleWorkerClient           = &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

func init() {
	Register(Command{
		Name:    "sample-worker",
		Summary: "refresh an internal sample-authoring worker session",
		Run:     sampleWorkerMain,
	})
}

func sampleWorkerMain(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "refresh" {
		fmt.Fprintln(sampleWorkerStderr, "usage: csx sample-worker refresh --server URL --token TOKEN")
		return 2
	}
	fs := flag.NewFlagSet("sample-worker refresh", flag.ContinueOnError)
	fs.SetOutput(sampleWorkerStderr)
	server := fs.String("server", "https://codesamplex.dev", "CodeSampleX server URL")
	token := fs.String("token", "", "sample-worker session token")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *token == "" {
		fmt.Fprintln(sampleWorkerStderr, "usage: csx sample-worker refresh --server URL --token TOKEN")
		return 2
	}
	base, err := sampleWorkerServerURL(*server)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker: %v\n", err)
		return 2
	}
	computerName, _ := os.Hostname()
	computerName = strings.TrimSpace(computerName)
	if len(computerName) > 120 {
		computerName = computerName[:120]
	}
	payload, _ := json.Marshal(map[string]string{"computerName": computerName})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/authoring/session/refresh", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker: invalid refresh request")
		return 2
	}
	req.Header.Set("Authorization", "Bearer "+*token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := sampleWorkerClient.Do(req)
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker: refresh failed; stop starting new sample work")
		return 1
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, sampleWorkerResponseLimit+1))
	if err != nil || len(body) > sampleWorkerResponseLimit {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker: invalid refresh response; stop starting new sample work")
		return 1
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker: session unavailable (HTTP %d); stop starting new sample work\n", resp.StatusCode)
		return 1
	}
	var result struct {
		IdleExpiresAt time.Time `json:"idleExpiresAt"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.IdleExpiresAt.IsZero() {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker: invalid refresh response; stop starting new sample work")
		return 1
	}
	fmt.Fprintf(sampleWorkerStdout, "sample-worker session active until %s\n", result.IdleExpiresAt.UTC().Format(time.RFC3339))
	return 0
}

func sampleWorkerServerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("server must be an origin URL")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1")) {
		return "", fmt.Errorf("server must use HTTPS")
	}
	return strings.TrimRight(u.String(), "/"), nil
}
