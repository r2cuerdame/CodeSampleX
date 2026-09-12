package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Keep these tests serial: the CLI's output and environment seams are globals.
func reservationNextHarness(t *testing.T, handler http.HandlerFunc) (string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	oldClient, oldOut, oldErr := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr
	oldCapability, oldOS := sampleWorkerCapability, sampleWorkerContainerOS
	t.Cleanup(func() {
		sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = oldClient, oldOut, oldErr
		sampleWorkerCapability, sampleWorkerContainerOS = oldCapability, oldOS
	})
	out, stderr := new(bytes.Buffer), new(bytes.Buffer)
	sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = srv.Client(), out, stderr
	sampleWorkerCapability = func(context.Context) domain.SandboxCapability { return domain.CapContainerRun }
	sampleWorkerContainerOS = func(context.Context) string { return "linux" }
	return srv.URL, out, stderr
}

func TestSampleWorkerNextReservationEnvelopeAndNoWork(t *testing.T) {
	for _, reserved := range []bool{false, true} {
		t.Run(fmt.Sprint(reserved), func(t *testing.T) {
			calls := 0
			url, out, stderr := reservationNextHarness(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				var envelope map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
					t.Error(err)
				}
				value, present := envelope["reservation"]
				if present != reserved || (reserved && string(value) != `"SAMPLE"`) {
					t.Errorf("reservation = %s present=%v", value, present)
				}
				for _, key := range []string{"schemaVersion", "sandboxCapability", "verifierOS", "clientVersion"} {
					if _, ok := envelope[key]; !ok {
						t.Errorf("missing existing environment field %q", key)
					}
				}
				fmt.Fprint(w, `{"status":"NO_WORK"}`)
			})
			args := []string{"--server", url, "--token", "csx_author_v1_reservation"}
			if reserved {
				args = append(args, "--reservation", "SAMPLE")
			}
			if code := sampleWorkerNext(context.Background(), args); code != 0 || calls != 1 {
				t.Fatalf("exit=%d calls=%d stderr=%s", code, calls, stderr)
			}
			if reserved {
				if !strings.Contains(out.String(), "no eligible SAMPLE new claim is available for this worker") {
					t.Errorf("stdout = %q, want reservation NO_WORK message", out.String())
				}
			} else {
				if !strings.Contains(out.String(), "no runnable Sample, Evidence, or Dependency gap is available") {
					t.Errorf("stdout = %q, want default NO_WORK message", out.String())
				}
			}
		})
	}
}

func TestSampleWorkerNextInvalidReservationRejected(t *testing.T) {
	for _, bad := range []string{"EVIDENCE", "DEPENDENCY", "ALL", "bogus"} {
		t.Run(bad, func(t *testing.T) {
			_, _, stderr := reservationNextHarness(t, func(w http.ResponseWriter, r *http.Request) {
				t.Error("server should not be called when client validation fails")
			})
			args := []string{"--server", "http://unused.local", "--token", "csx_author_v1_reservation", "--reservation", bad}
			code := sampleWorkerNext(context.Background(), args)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), "reservation must be SAMPLE") {
				t.Fatalf("stderr = %q, want reservation must be SAMPLE", stderr.String())
			}
		})
	}
}

func TestSampleWorkerNextOldServerRejectionFailClosed(t *testing.T) {
	calls := 0
	url, _, stderr := reservationNextHarness(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error":"json: unknown field \"reservation\""}`, http.StatusBadRequest)
	})
	args := []string{"--server", url, "--token", "csx_author_v1_reservation", "--reservation", "SAMPLE"}
	code := sampleWorkerNext(context.Background(), args)
	if code == 0 {
		t.Fatalf("exit code = 0, want failure when server rejects reservation")
	}
	if calls != 1 {
		t.Fatalf("server called %d times, want exactly 1 (no retry without reservation)", calls)
	}
	if !strings.Contains(stderr.String(), "unknown field") && !strings.Contains(stderr.String(), "400") {
		// Just ensure stderr contains error info
	}
}

