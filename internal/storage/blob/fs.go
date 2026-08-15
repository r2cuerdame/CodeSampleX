package blob

import (
	"context"
	"io"

	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
)

// FS adapts the local CAS store to the Store interface (the "LocalCAS"
// provider from goal.md §15.4).
type FS struct{ cas *cas.Store }

// NewFS opens a filesystem-backed store rooted at dir.
func NewFS(dir string) (*FS, error) {
	c, err := cas.Open(dir)
	if err != nil {
		return nil, err
	}
	return &FS{cas: c}, nil
}

func (f *FS) Put(_ context.Context, r io.Reader) (string, error)      { return f.cas.Put(r) }
func (f *FS) Get(_ context.Context, id string) (io.ReadCloser, error) { return f.cas.Get(id) }
func (f *FS) Has(_ context.Context, id string) (bool, error)          { return f.cas.Has(id), nil }
func (f *FS) Delete(_ context.Context, id string) error               { return f.cas.Delete(id) }
func (f *FS) TotalSize(_ context.Context) (int64, error)              { return f.cas.TotalSize() }
