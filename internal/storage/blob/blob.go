// Package blob abstracts sample-artifact storage (goal.md §14.2, §15.4).
// v1 ships the local content-addressed filesystem implementation; object
// storage / OCI / peer-backed stores plug in behind the same interface
// later without protocol changes.
package blob

import (
	"context"
	"io"
)

// Store holds immutable content-addressed blobs ("sha256:<hex>" ids).
type Store interface {
	Put(ctx context.Context, r io.Reader) (id string, err error)
	Get(ctx context.Context, id string) (io.ReadCloser, error)
	Has(ctx context.Context, id string) (bool, error)
	Delete(ctx context.Context, id string) error
	TotalSize(ctx context.Context) (int64, error)
}
