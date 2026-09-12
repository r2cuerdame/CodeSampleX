package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func TestEvidenceStatsCLIProcessHelper(t *testing.T) {
	if os.Getenv("CSX_TEST_EVIDENCE_STATS_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(Main(os.Args[i+1:]))
		}
	}
	os.Exit(2)
}

func TestEvidenceStatsFullCLIProcessBypassesStartupWriterAndPreservesStamp(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "csx.db")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for key, value := range map[string]string{
		"firstRunAt": "2026-09-11T00:00:00Z", "lastUpload": "2026-09-12T00:01:00Z",
		"lastUploadAttempt": "2026-09-12T00:02:00Z",
		"lastUploadError":   "evidence: the server refused 2 batches, first: PRIVATE_CANARY",
	} {
		if err := db.SetStat(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Enqueue(ctx, "receipt", "PRIVATE_PAYLOAD"); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `INSERT INTO observations(epoch,purl,symbol,env_hash,stage,result,count,error_fp,uploaded)
		VALUES('2026-09-12','pkg:npm/private@1','','env','TEST','PASS',1,'',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK")
	// Exercise the whole dispatcher in a separate process. A stamp or fallback
	// Open regression waits on this writer and is killed by the test deadline.
	for _, args := range [][]string{{"stats", "--evidence-only"}, {"stats", "--evidence-only", "--json"},
		{"--debug", "stats", "--json", "--evidence-only"}} {
		runCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		command := exec.CommandContext(runCtx, os.Args[0], append([]string{"-test.run=^TestEvidenceStatsCLIProcessHelper$", "--"}, args...)...)
		command.Env = append(os.Environ(), "CSX_TEST_EVIDENCE_STATS_PROCESS=1", "CSX_HOME="+home)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		started := time.Now()
		err := command.Run()
		elapsed := time.Since(started)
		cancel()
		if err != nil {
			t.Fatalf("read-only full CLI blocked/failed after %s: %v, stderr=%q", elapsed, err, stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), "PRIVATE_") || stderr.Len() != 0 {
			t.Fatalf("private output escaped: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		if strings.Contains(strings.Join(args, " "), "--json") {
			var st localdb.EvidenceStats
			if err := json.Unmarshal(stdout.Bytes(), &st); err != nil || st.QueueDepth != 2 || st.Queue.EvidenceBatches != 1 ||
				st.Queue.Uploads != 1 || st.LastUpload != "2026-09-12T00:01:00Z" || st.LastUploadError != "evidence: the server refused 2 batches" {
				t.Fatalf("wrong narrow JSON: %s, %v", stdout.String(), err)
			}
		} else {
			for _, pattern := range []string{`(?m)^  Pending evidence batches: +1$`, `(?m)^  Pending upload reports: +1$`,
				`(?m)^  Pending queue depth: +2$`, `(?m)^Last upload attempt: +2026-09-12T00:02:00Z$`,
				`(?m)^Last upload error: +evidence: the server refused 2 batches$`} {
				if !regexp.MustCompile(pattern).Match(stdout.Bytes()) {
					t.Fatalf("Farm-readable output missing %s: %s", pattern, stdout.String())
				}
			}
		}
		for _, forbidden := range []string{"Readiness", "Estimated reasoning", "cacheBytes", "retrievalQuality"} {
			if strings.Contains(stdout.String(), forbidden) {
				t.Fatalf("full dashboard entered evidence path: %q", forbidden)
			}
		}
	}
	var first string
	if err := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='stat:firstRunAt'`).Scan(&first); err != nil || first != "2026-09-11T00:00:00Z" {
		t.Fatalf("first stamp changed: %q, %v", first, err)
	}
}

func TestEvidenceStatsOnlyExactInvocationsSkipActivation(t *testing.T) {
	for _, args := range [][]string{{"stats", "--evidence-only"}, {"stats", "--json", "--evidence-only"},
		{"stats", "--evidence-only", "--json"}, {"--debug", "stats", "--evidence-only"}} {
		home := filepath.Join(t.TempDir(), "absent")
		t.Setenv("CSX_HOME", home)
		out, code := captureStdout(t, func() int { return Main(args) })
		if code != 1 || out != "csx: evidence stats unavailable\n" {
			t.Fatalf("missing home result = %d, %q", code, out)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatalf("read-only command created a home: %v", err)
		}
	}
	for _, args := range [][]string{{"stats", "--evidence-only", "extra"}, {"stats", "--evidence-only", "--evidence-only"},
		{"stats", "--json", "--json", "--evidence-only"}, {"version"}, {"help"}, {"not-a-command"}} {
		home := t.TempDir()
		t.Setenv("CSX_HOME", home)
		captureStdout(t, func() int { return Main(args) })
		if readLedger(t, home).FirstRunAt.IsZero() {
			t.Fatalf("normal/invalid invocation lost activation behavior: %v", args)
		}
	}
}

func TestEvidenceStatsDoesNotCreateMissingActivationOrUseConfigDaemonCAS(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A malformed config would fail daemon.New; the narrow read has no reason
	// to load it, query a daemon, or enumerate CAS content.
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("INVALID_PRIVATE_CONFIG"), 0600); err != nil {
		t.Fatal(err)
	}
	out, code := captureStdout(t, func() int { return Main([]string{"stats", "--evidence-only", "--json"}) })
	if code != 0 || !strings.Contains(out, `"queueDepth":0`) {
		t.Fatalf("narrow read failed: %d, %s", code, out)
	}
	if _, ok, err := db.GetStat(context.Background(), "firstRunAt"); err != nil || ok {
		t.Fatalf("diagnostic stamped activation: %t, %v", ok, err)
	}
	for _, name := range []string{"cas", "logs", "samples"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("diagnostic initialized %s: %v", name, err)
		}
	}
}

func TestEvidenceStatsFailurePrintsNoPartialCountersOrPrivateError(t *testing.T) {
	home := filepath.Join(t.TempDir(), "PRIVATE_CANARY_HOME")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CSX_HOME", home)
	path := filepath.Join(home, "csx.db")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetStat(context.Background(), "lastUpload", "PRIVATE_CANARY_INVALID_TIME"); err != nil {
		t.Fatal(err)
	}
	for _, jsonOut := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		if code := evidenceStatsMain(context.Background(), jsonOut, &stdout, &stderr); code != 1 || stdout.Len() != 0 ||
			stderr.String() != "csx: evidence stats unavailable\n" {
			t.Fatalf("failure output: %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
}

// Review blocker 5184582285: CLI-level negative controls mirror the accessor
// tests and confirm the bounded BLOB validation causes empty stdout + fixed
// unavailable on stderr when NUL-embedded metadata reaches the CLI surface.
func TestEvidenceStatsNULEmbeddedMetadataRejectedAtCLI(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		raw       []byte
	}{
		{"timestamp+NUL_suffix", "lastUpload",
			append([]byte("2026-09-12T00:01:00Z\x00hidden-payload"), []byte{}...)},
		{"NUL-leading_error", "lastUploadError",
			append([]byte("\x00evidence: the server refused 1 batch: private"), []byte{}...)},
		{">512-rune_error_embedded_NUL", "lastUploadError",
			append(append([]byte(strings.Repeat("A", 256)), 0), []byte(strings.Repeat("B", 257))...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CSX_HOME", home)
			path := filepath.Join(home, "csx.db")
			db, err := localdb.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			// Insert via CAST(? AS TEXT) so NUL bytes survive into the TEXT
			// column; the Go sqlite driver may truncate string args at NUL.
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.ExecContext(context.Background(),
				`INSERT INTO meta(key, value) VALUES(?, CAST(? AS TEXT))
				ON CONFLICT(key) DO UPDATE SET value = CAST(excluded.value AS TEXT)`,
				"stat:"+tc.key, tc.raw); err != nil {
				t.Fatal(err)
			}
			for _, jsonOut := range []bool{false, true} {
				var stdout, stderr bytes.Buffer
				if code := evidenceStatsMain(context.Background(), jsonOut, &stdout, &stderr); code != 1 || stdout.Len() != 0 ||
					stderr.String() != "csx: evidence stats unavailable\n" {
					t.Fatalf("NUL-embedded %s (json=%t): code=%d stdout=%q stderr=%q",
						tc.name, jsonOut, code, stdout.String(), stderr.String())
				}
			}
		})
	}
}

type evidenceStatsBrokenWriter struct{}

func (evidenceStatsBrokenWriter) Write([]byte) (int, error) {
	return 0, errors.New("PRIVATE_OUTPUT_FAILURE")
}

func TestEvidenceStatsOutputFailureIsNotSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, jsonOut := range []bool{false, true} {
		var stderr bytes.Buffer
		if code := evidenceStatsMain(context.Background(), jsonOut, evidenceStatsBrokenWriter{}, &stderr); code != 1 || stderr.Len() != 0 {
			t.Fatalf("output failure reported success/private error: %d, %s", code, stderr.String())
		}
	}
}
