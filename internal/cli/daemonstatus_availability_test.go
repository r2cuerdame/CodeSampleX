package cli

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// `csx daemon status` printed "queue depth: 0" whether the queue was empty
// or unreadable (#377). The number is only a measurement when it was one.
func TestDaemonStatusPrintsUnavailableWhenTheQueueCannotBeRead(t *testing.T) {
	home := newCLIHome(t, nil)
	d := startCLIDaemon(t, home)
	// An initialized home: the first-run stamp exists, so the CLI's
	// activation read does not open a migrating writer that would quietly
	// rebuild the index this test removes.
	if err := d.DB.StampFirst(t.Context(), localdb.StatFirstRunAt, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(home, "csx.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP INDEX observations_pending`); err != nil {
		t.Fatal(err)
	}

	out, code := captureStdout(t, func() int {
		return Main([]string{"daemon", "status"})
	})
	if code != 0 || !strings.Contains(out, "running") {
		t.Fatalf("daemon status exit=%d output=%q", code, out)
	}
	if strings.Contains(out, "queue depth: 0") {
		t.Fatalf("an unreadable queue was printed as a measured zero:\n%s", out)
	}
	if !strings.Contains(out, "queue depth: unavailable") {
		t.Fatalf("status did not say the queue was unreadable:\n%s", out)
	}
}
