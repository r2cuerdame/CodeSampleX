package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Farm health must still read the committed queue and refusal state while
// ordinary evidence ingestion owns SQLite's WAL write reservation.
func TestStatsReadsCommittedHealthWhileEvidenceWriterIsActive(t *testing.T) {
	for _, liveDaemon := range []bool{false, true} {
		name := "fallback"
		if liveDaemon {
			name = "daemon"
		}
		t.Run(name, func(t *testing.T) {
			home := newCLIHome(t, func(c *config.Config) { c.DaemonPort = 1 })
			if liveDaemon {
				cfg, err := config.Load(home)
				if err != nil {
					t.Fatal(err)
				}
				cfg.DaemonPort = 0
				if err := cfg.Save(home); err != nil {
					t.Fatal(err)
				}
				startCLIDaemon(t, home)
			}
			db, err := localdb.Open(filepath.Join(home, "csx.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.StampFirst(t.Context(), localdb.StatFirstRunAt, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := db.SetStat(t.Context(), "lastUploadError", "evidence refusal remains visible"); err != nil {
				t.Fatal(err)
			}
			writer, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(home, "csx.db")))
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			writer.SetMaxOpenConns(1)
			for _, q := range []string{
				`INSERT INTO upload_queue(kind,payload,created_at) VALUES('search-hit','{}','2026-09-13T15:00:00Z')`,
				`INSERT INTO refused_evidence(epoch,purl,refused_at) VALUES('2026-09-13','pkg:npm/example@1.0.0','2026-09-13T15:00:00Z')`,
				`BEGIN IMMEDIATE`,
			} {
				if _, err := writer.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			defer writer.Exec(`ROLLBACK`)
			blocked := false
			out, code := captureStdout(t, func() int {
				done := make(chan int, 1)
				go func() { done <- Main([]string{"stats", "--json"}) }()
				select {
				case code := <-done:
					return code
				case <-time.After(2 * time.Second):
					blocked = true
					_, _ = writer.ExecContext(context.Background(), `ROLLBACK`)
					return <-done
				}
			})
			if blocked {
				t.Error("stats waited for an evidence write lock before it could read health")
			}
			if code != 0 {
				t.Fatalf("stats exit=%d output=%s", code, out)
			}
			var st daemon.Stats
			if err := json.Unmarshal([]byte(out), &st); err != nil {
				t.Fatal(err)
			}
			if st.QueueDepth != 1 || st.EvidenceRefusedTerminal != 1 || st.LastUploadError != "evidence refusal remains visible" {
				t.Fatalf("health signals were lost: %+v", st)
			}
		})
	}
}

func TestStatsCannotReportHealthyZerosWhenQueueStateIsUnreadable(t *testing.T) {
	for _, table := range []string{"upload_queue", "refused_evidence"} {
		t.Run(table, func(t *testing.T) {
			home := newCLIHome(t, func(c *config.Config) { c.DaemonPort = 1 })
			db, err := localdb.Open(filepath.Join(home, "csx.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.StampFirst(t.Context(), localdb.StatFirstRunAt, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			writer, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(home, "csx.db")))
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err := writer.Exec("DROP TABLE " + table); err != nil {
				t.Fatal(err)
			}
			out, code := captureStdout(t, func() int { return Main([]string{"stats", "--json"}) })
			if code == 0 || json.Valid([]byte(out)) {
				t.Fatalf("unreadable health became success: code=%d output=%s", code, out)
			}
		})
	}
}
