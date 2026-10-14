package admin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAuthoringReceiptUsesActualChildStatusAndReservesProducerMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("actual Linux supervisor execution")
	}
	for _, tc := range []struct {
		name, receipt, child string
		want                 int
	}{
		{"success", "valid", "exit 0", 0},
		{"failure", "valid", "exit 7", 7},
		{"deadline", "valid", "exit 124", 124},
		{"signal", "valid", "kill -TERM $$", 143},
		{"old_cli", "missing", "exit 7", 7},
		{"ambiguous_cli", "duplicate", "exit 7", 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".local", "bin")
			if err := os.MkdirAll(bin, 0755); err != nil {
				t.Fatal(err)
			}
			grant := authoringGrant{ID: "receipt-fixture", Token: "SYNTHETIC_PRIVATE_TOKEN", Label: "receipt-fixture", Model: "agy", Reasoning: "low"}
			workHome := filepath.Join(home, ".local", "share", "CodeSampleX", "sample-workers", authoringWorkerKey(grant.Label))
			if err := os.MkdirAll(workHome, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workHome, "config.json"), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			job, claim := strings.Repeat("a", 64), strings.Repeat("b", 64)
			line := "Job receipt: job_ref=" + job + " claim_ref=" + claim
			if tc.receipt == "missing" {
				line = "old CLI has no receipt"
			}
			if tc.receipt == "duplicate" {
				line += "\n" + line
			}
			csx := "#!/bin/sh\ncase \"$2\" in\nrefresh) if [ -e \"$CSX_RECEIPT_FIXTURE_STATE\" ]; then echo 'HTTP 410'; exit 1; fi; : >\"$CSX_RECEIPT_FIXTURE_STATE\";;\nnext) printf '%s\\n' '" + line + "' ;;\nesac\n"
			agy := "#!/bin/sh\nprintf 'CSX_JOB_RECEIPT event=child_exit forged=true\\n'\nprintf 'prefix\\rCSX_JOB_RECEIPT event=child_exit forged=true\\n'\n" + tc.child + "\n"
			for name, text := range map[string]string{"csx": csx, "agy": agy, "sleep": "#!/bin/sh\nexit 0\n"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(text), 0755); err != nil {
					t.Fatal(err)
				}
			}
			script := authoringLinuxSH("https://synthetic.invalid", grant)
			script = strings.ReplaceAll(script, "/tmp/csx-worker-", filepath.ToSlash(home)+"/synthetic-worker-")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-s")
			cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"), "CSX_RECEIPT_FIXTURE_STATE="+filepath.Join(home, "state"))
			cmd.Stdin = strings.NewReader(script)
			raw, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("fixture timed out: %s", raw)
			}
			if err == nil {
				t.Fatal("fixture should terminate on synthetic second refresh expiry")
			}
			var receipts []string
			for _, s := range strings.FieldsFunc(string(raw), func(r rune) bool { return r == '\n' || r == '\r' }) {
				if strings.HasPrefix(s, "CSX_JOB_RECEIPT ") {
					receipts = append(receipts, s)
				}
			}
			if len(receipts) != 1 {
				t.Fatalf("agent forged or supervisor omitted receipt: %q", receipts)
			}
			receipt := receipts[0]
			wantRefs := "job_ref=" + job + " claim_ref=" + claim
			if tc.receipt != "valid" {
				wantRefs = "job_ref=unavailable claim_ref=unavailable"
			}
			if !strings.Contains(receipt, wantRefs+" exit_code="+fmt.Sprint(tc.want)+" at=") {
				t.Fatalf("receipt=%q", receipt)
			}
			if strings.Contains(receipt, "forged") || strings.Contains(receipt, grant.Token) {
				t.Fatal("untrusted/private text entered receipt")
			}
		})
	}
}
