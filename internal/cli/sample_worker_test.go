package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestSampleWorkerRefreshUsesCompleteCLICommand(t *testing.T) {
	const token = "csx_author_v1_secret"
	expires := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/authoring/session/refresh" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("authorization = %q", got)
		}
		var body struct {
			ComputerName string `json:"computerName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ComputerName) == "" {
			t.Fatalf("refresh body = %+v, err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"idleExpiresAt":%q}`, expires.Format(time.RFC3339))
	}))
	defer srv.Close()

	oldClient, oldOut, oldErr := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr
	t.Cleanup(func() { sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = oldClient, oldOut, oldErr })
	sampleWorkerClient = srv.Client()
	var out, stderr bytes.Buffer
	sampleWorkerStdout, sampleWorkerStderr = &out, &stderr
	if code := sampleWorkerMain(context.Background(), []string{"refresh", "--server", srv.URL, "--token", token}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(out.String(), expires.Format(time.RFC3339)) {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestSampleWorkerRefreshFailsClosed(t *testing.T) {
	for _, server := range []string{"http://example.com", "https://user@example.com", "https://example.com/path"} {
		if _, err := sampleWorkerServerURL(server); err == nil {
			t.Errorf("accepted server %q", server)
		}
	}
}

func TestSampleWorkerNextPrintsExactProposeCommand(t *testing.T) {
	const token = "csx_author_v1_next-test"
	lease := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/authoring/work/next" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var profile struct {
			SchemaVersion     int                      `json:"schemaVersion"`
			SandboxCapability domain.SandboxCapability `json:"sandboxCapability"`
			VerifierOS        []string                 `json:"verifierOS"`
		}
		if err := json.NewDecoder(r.Body).Decode(&profile); err != nil || profile.SchemaVersion != 1 || profile.SandboxCapability != domain.CapContainerRun || len(profile.VerifierOS) != 1 || profile.VerifierOS[0] != "linux" {
			t.Fatalf("profile = %+v err=%v", profile, err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ASSIGNED","work":{"package":"pkg:maven/org.apache.commons/commons-lang3@3.17.0","symbol":"org.apache.commons.lang3.StringUtils.isBlank","asks":4,"leaseExpiresAt":%q}}`, lease.Format(time.RFC3339))
	}))
	defer srv.Close()

	oldClient, oldOut, oldErr, oldCapability := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr, sampleWorkerCapability
	oldContainerOS := sampleWorkerContainerOS
	t.Cleanup(func() {
		sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr, sampleWorkerCapability = oldClient, oldOut, oldErr, oldCapability
		sampleWorkerContainerOS = oldContainerOS
	})
	sampleWorkerClient = srv.Client()
	sampleWorkerCapability = func(context.Context) domain.SandboxCapability { return domain.CapContainerRun }
	// The envelope now reports the daemon's container mode, so this test would
	// otherwise assert whatever the machine running it happens to serve — and
	// it fails on a Windows CI runner, where Docker defaults to Windows
	// containers. What this test is about is the propose command it prints.
	// sampleWorkerContainerOS is covered on its own in windowsevidence_test.go.
	sampleWorkerContainerOS = func(context.Context) string { return "linux" }
	var out, stderr bytes.Buffer
	sampleWorkerStdout, sampleWorkerStderr = &out, &stderr
	if code := sampleWorkerMain(context.Background(), []string{"next", "--server", srv.URL, "--token", token}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"4 asks", "pkg:maven/org.apache.commons/commons-lang3@3.17.0", "org.apache.commons.lang3.StringUtils.isBlank", "csx sample propose --goal"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("next output missing %q: %s", want, out.String())
		}
	}
}

func TestSampleWorkerNextPrintsAxisSpecificCompletionInstructions(t *testing.T) {
	const token = "csx_author_v1_axis-test"
	lease := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	oldClient, oldOut, oldErr, oldCapability := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr, sampleWorkerCapability
	oldContainerOS := sampleWorkerContainerOS
	t.Cleanup(func() {
		sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr, sampleWorkerCapability = oldClient, oldOut, oldErr, oldCapability
		sampleWorkerContainerOS = oldContainerOS
	})
	sampleWorkerCapability = func(context.Context) domain.SandboxCapability { return domain.CapContainerRun }
	sampleWorkerContainerOS = func(context.Context) string { return "linux" }

	for _, tc := range []struct {
		axis, heading, instruction string
	}{
		{"EVIDENCE", "Assigned Evidence completeness work", "ordinary resolve/build"},
		{"DEPENDENCY", "Assigned Dependency completeness work", "lockfile exists"},
	} {
		t.Run(tc.axis, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"status":"ASSIGNED","work":{"package":"pkg:npm/axios@1.12.0","kind":"EXPANSION","axis":%q,"score":99,"leaseExpiresAt":%q}}`, tc.axis, lease.Format(time.RFC3339))
			}))
			defer srv.Close()
			sampleWorkerClient = srv.Client()
			var out, stderr bytes.Buffer
			sampleWorkerStdout, sampleWorkerStderr = &out, &stderr
			if code := sampleWorkerMain(context.Background(), []string{"next", "--server", srv.URL, "--token", token}); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr.String())
			}
			for _, want := range []string{tc.heading, "Axis: " + tc.axis, "Produce this axis, not a sample", tc.instruction} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output missing %q: %s", want, out.String())
				}
			}
			if strings.Contains(out.String(), "csx sample propose") || strings.Contains(out.String(), "no-callable-symbol") {
				t.Errorf("non-Sample work printed Sample-only actions: %s", out.String())
			}
		})
	}
}

func TestSampleWorkerSubmitUploadsLocalDraftWithoutPublishing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	sampleID := createLocalSample(t, home, nil)
	const token = "csx_author_v1_submit-test-only"
	var gotManifest, gotStatus, gotArtifact string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/authoring/drafts" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("authorization = %q", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		gotManifest, gotStatus = r.FormValue("manifest"), r.FormValue("localStatus")
		file, _, err := r.FormFile("artifact")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		artifact, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		gotArtifact = fmt.Sprintf("sha256:%x", sha256.Sum256(artifact))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"sampleId":%q,"status":"PRIVATE_DRAFT"}`, sampleID)
	}))
	defer srv.Close()

	oldClient, oldOut, oldErr := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr
	t.Cleanup(func() { sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = oldClient, oldOut, oldErr })
	sampleWorkerClient = srv.Client()
	var out, stderr bytes.Buffer
	sampleWorkerStdout, sampleWorkerStderr = &out, &stderr
	if code := sampleWorkerMain(context.Background(), []string{"submit", sampleID, "--server", srv.URL, "--token", token}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if gotArtifact != sampleID || gotStatus != "LOCAL" || !strings.Contains(gotManifest, `"goal"`) {
		t.Fatalf("upload artifact=%q status=%q manifest=%q", gotArtifact, gotStatus, gotManifest)
	}
	if !strings.Contains(out.String(), "Private sample draft submitted") {
		t.Fatalf("stdout = %q", out.String())
	}
}

// TestSampleWorkerReadsTokenFromEnvironment is the client half of
// CodeSampleX-Farm#14: with no --token on the command line, the session bearer
// is taken from CSX_SESSION_TOKEN, so a worker script never has to place it in
// argv. The flag still wins when both are present.
func TestSampleWorkerReadsTokenFromEnvironment(t *testing.T) {
	const envToken = "csx_author_v1_env_only"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"idleExpiresAt":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer srv.Close()

	oldClient, oldOut, oldErr := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr
	t.Cleanup(func() { sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = oldClient, oldOut, oldErr })
	sampleWorkerClient = srv.Client()
	var out, stderr bytes.Buffer
	sampleWorkerStdout, sampleWorkerStderr = &out, &stderr

	// No --token argument at all.
	t.Setenv("CSX_SESSION_TOKEN", envToken)
	if code := sampleWorkerMain(context.Background(), []string{"refresh", "--server", srv.URL}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if gotAuth != "Bearer "+envToken {
		t.Fatalf("authorization = %q, want the token from the environment", gotAuth)
	}

	// An explicit --token still overrides the environment.
	if code := sampleWorkerMain(context.Background(), []string{"refresh", "--server", srv.URL, "--token", "csx_author_v1_flag_wins"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if gotAuth != "Bearer csx_author_v1_flag_wins" {
		t.Fatalf("authorization = %q, want the flag to win over the environment", gotAuth)
	}
}

// TestSampleWorkerNoTokenAnywhereFailsClosed keeps the missing-token guard: no
// flag and no environment variable is still a usage error, not a request with
// an empty bearer.
func TestSampleWorkerNoTokenAnywhereFailsClosed(t *testing.T) {
	oldOut, oldErr := sampleWorkerStdout, sampleWorkerStderr
	t.Cleanup(func() { sampleWorkerStdout, sampleWorkerStderr = oldOut, oldErr })
	var out, stderr bytes.Buffer
	sampleWorkerStdout, sampleWorkerStderr = &out, &stderr
	t.Setenv("CSX_SESSION_TOKEN", "")
	if code := sampleWorkerMain(context.Background(), []string{"refresh", "--server", "https://codesamplex.dev"}); code != 2 {
		t.Fatalf("exit = %d, want 2 for a missing token", code)
	}
}
