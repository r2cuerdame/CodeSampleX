package cas

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// TotalSizeContext is read-only optional accounting. It never returns a partial
// byte count as a valid result. ReadDir batches avoid loading/sorting a complete
// directory before observing cancellation. Legacy quota/eviction scans are
// intentionally unchanged.
func (s *Store) TotalSizeContext(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	root := filepath.Join(s.root, "sha256")
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("cas: object root is not a directory")
	}
	return totalSizeFS(ctx, os.DirFS(root))
}
func totalSizeFS(ctx context.Context, store fs.FS) (int64, error) {
	var total int64
	var walk func(string) error
	walk = func(path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := store.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		dir, ok := f.(fs.ReadDirFile)
		if !ok {
			return fmt.Errorf("cas: object directory unavailable")
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, err := dir.ReadDir(256)
			if err != nil && err != io.EOF {
				return err
			}
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				name := entry.Name()
				if path != "." {
					name = path + "/" + name
				}
				if entry.IsDir() {
					if err := walk(name); err != nil {
						return err
					}
					continue
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				total += info.Size()
			}
			if err == io.EOF {
				return nil
			}
		}
	}
	if err := walk("."); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return total, nil
}
