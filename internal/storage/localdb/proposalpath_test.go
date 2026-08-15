package localdb

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// `csx sample pending` prints the absolute workdir and tells the user to
// run `csx sample create <workdir>`. A user who cds into it and types
// `csx sample create .` — the obvious thing — updated zero rows, because
// the clear is an exact string match against the path the clean room
// generated. ExecContext returns nil for zero rows, so nothing was printed
// either, and pending kept telling them to create a sample they had
// already created. Forever.
func TestAProposalIsClearedByAnyPathThatMeansIt(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	work := t.TempDir()
	if err := db.SaveProposal(ctx, ProposalRow{
		Workdir: work, Goal: "post json", CreatedAt: time.Now().UTC(), State: "pending",
	}); err != nil {
		t.Fatal(err)
	}

	// The user is inside the workspace and types ".".
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Skipf("cannot enter the temp dir: %v", err)
	}
	defer os.Chdir(old) //nolint:errcheck

	if err := db.SetProposalState(ctx, ".", "created"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.PendingProposals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if filepath.Clean(r.Workdir) == filepath.Clean(work) {
			t.Error("the proposal is still offered as pending after being acted on")
		}
	}
}
