package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/samples"
)

// workspaceIdentityFile is what a workspace writes about itself.
//
// A separate file rather than a field in csx.json, because csx.json is the
// MANIFEST -- it travels inside the artifact and its bytes decide the sample
// id. Recording where a sample was authored inside the thing being addressed
// would make the same sources produce different ids on different machines.
//
// The name comes from the packer, which is what has to leave it out; keeping
// one definition is what stops a rename here from silently putting it back
// into artifacts.
const workspaceIdentityFile = samples.WorkspaceIdentityFile

// workspaceIdentity is the answer to "which sample did this directory
// become?", which nothing on a node could previously give.
type workspaceIdentity struct {
	SchemaVersion int    `json:"schemaVersion"`
	SampleID      string `json:"sampleId"`
	CreatedAt     string `json:"createdAt"`
}

// writeWorkspaceIdentity records the sample a directory became, replacing any
// earlier answer.
//
// Replacing rather than appending: a tree naming two samples is worse than
// one naming none, because a collector would believe the stale one. Written
// whole through a temporary file so a reader never sees half an answer.
func writeWorkspaceIdentity(dir, sampleID string, now time.Time) error {
	body, err := json.MarshalIndent(workspaceIdentity{
		SchemaVersion: 1,
		SampleID:      sampleID,
		CreatedAt:     now.Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, byte(10))
	tmp, err := os.CreateTemp(dir, samples.WorkspaceTempPrefix+"*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, workspaceIdentityFile)); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
