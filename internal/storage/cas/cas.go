// Package cas implements a content-addressed store on the local filesystem.
// Objects are identified by "sha256:<hex>" and stored under
// <root>/sha256/<hex[0:2]>/<hex[2:4]>/<hex>. Writes go to a temp file in the
// root and are renamed into place, so a crash never leaves a partial object
// at its final path.
package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const idPrefix = "sha256:"

// Store is a content-addressed object store rooted at a directory.
type Store struct {
	root string
}

// Open creates the root directory if needed and returns a Store over it.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "sha256"), 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// parseID validates id ("sha256:" + 64 lowercase hex chars) and returns the hex part.
func parseID(id string) (string, error) {
	if len(id) != len(idPrefix)+64 || id[:len(idPrefix)] != idPrefix {
		return "", fmt.Errorf("cas: invalid id %q", id)
	}
	h := id[len(idPrefix):]
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("cas: invalid id %q", id)
		}
	}
	return h, nil
}

func (s *Store) pathFor(hexDigest string) string {
	return filepath.Join(s.root, "sha256", hexDigest[0:2], hexDigest[2:4], hexDigest)
}

// Put streams r into the store and returns its content id.
// Storing content that already exists is a cheap no-op.
func (s *Store) Put(r io.Reader) (string, error) {
	tmp, err := os.CreateTemp(s.root, "tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}

	hexDigest := hex.EncodeToString(h.Sum(nil))
	id := idPrefix + hexDigest
	dst := s.pathFor(hexDigest)
	// A file already at this path was taken as proof the object was stored,
	// without ever reading it. The one promise this store makes is that the
	// id IS the sha256 of the bytes it hands back, and a file that has been
	// truncated, half-written by an older build, or damaged on disk keeps
	// that path occupied forever: Put reports success, and every later Get
	// serves corrupt bytes under a content address that says they are
	// intact. We hold a verified copy right here, so check before trusting.
	if verified, err := objectMatches(dst, hexDigest); err == nil && verified {
		return id, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	// Rename cannot replace an existing file on Windows, and the file that
	// is there is one we just found does not match its own address.
	_ = os.Remove(dst)
	if err := os.Rename(tmpName, dst); err != nil {
		// A concurrent Put may have won the rename. Identical content is
		// the expected case — but the same rule applies: verify it.
		if verified, verr := objectMatches(dst, hexDigest); verr == nil && verified {
			return id, nil
		}
		return "", err
	}
	return id, nil
}

// objectMatches reports whether the file at path hashes to hexDigest.
// A missing file is (false, nil): not an error, just not there.
func objectMatches(path, hexDigest string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == hexDigest, nil
}

// Get opens the object for reading. The error satisfies os.IsNotExist for
// missing objects; malformed ids return a non-nil error as well.
func (s *Store) Get(id string) (io.ReadCloser, error) {
	h, err := parseID(id)
	if err != nil {
		return nil, err
	}
	return os.Open(s.pathFor(h))
}

// Has reports whether the object exists. Malformed ids report false.
func (s *Store) Has(id string) bool {
	h, err := parseID(id)
	if err != nil {
		return false
	}
	_, err = os.Stat(s.pathFor(h))
	return err == nil
}

// Delete removes the object. Missing objects return an error satisfying
// os.IsNotExist; malformed ids return a non-nil error.
func (s *Store) Delete(id string) error {
	h, err := parseID(id)
	if err != nil {
		return err
	}
	p := s.pathFor(h)
	if _, err := os.Stat(p); err != nil {
		return err
	}
	return os.Remove(p)
}

// TotalSize returns the sum of all stored object sizes in bytes.
func (s *Store) TotalSize() (int64, error) {
	var total int64
	err := s.walkObjects(func(path string, info fs.FileInfo) error {
		total += info.Size()
		return nil
	})
	return total, err
}

// List returns the ids of every stored object, in no particular order.
func (s *Store) List() ([]string, error) {
	ids := []string{}
	err := s.walkObjects(func(path string, info fs.FileInfo) error {
		if _, perr := parseID(idPrefix + filepath.Base(path)); perr == nil {
			ids = append(ids, idPrefix+filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) walkObjects(fn func(path string, info fs.FileInfo) error) error {
	return filepath.WalkDir(filepath.Join(s.root, "sha256"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return fn(path, info)
	})
}
