// Package samples implements the local half of the public-sample pipeline
// (goal.md §9, plan C13): canonical content-addressed artifacts, leakage
// scanning, sanitized clean-room specs, and the create workflow. Nothing in
// this package ever transmits data; publishing is a separate, explicit step.
package samples

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Artifact limits (goal.md §16.4, plan C13).
const (
	// MaxCompressedBytes caps the canonical tar.gz size.
	MaxCompressedBytes = 256 * 1024
	// MaxFiles caps the number of regular files in a sample.
	MaxFiles = 200
	// maxUnpackedBytes bounds decompression on Unpack (bomb guard).
	maxUnpackedBytes = 8 << 20
	// maxUnpackEntries bounds total tar entries (files + dirs) on Unpack.
	maxUnpackEntries = 2 * MaxFiles
)

// forbiddenDirNames are directory names that must never appear in a sample
// (generated/output/VCS trees; goal.md §7.5, task C13 + .venv/dist).
var forbiddenDirNames = map[string]bool{
	"node_modules": true,
	".git":         true,
	"venv":         true,
	".venv":        true,
	"target":       true,
	"dist":         true,
}

func forbiddenDir(seg string) bool { return forbiddenDirNames[strings.ToLower(seg)] }

// forbiddenRootDirNames are resolve output directories that are only
// generated at the project root. They are matched at the root ONLY: a
// sample may legitimately contain src/vendor/ or lib/deps/, and rejecting
// those names at any depth would block honest samples to catch a mistake
// that can only happen one level up.
var forbiddenRootDirNames = map[string]bool{
	"vendor":        true, // composer, and go's vendored module tree
	"deps":          true, // mix
	"_build":        true, // mix
	".dart_tool":    true, // dart pub
	".csx-vendor":   true, // this project's own two-phase resolve output
	".bundle":       true, // bundler config written during install
	"__pycache__":   true,
	".pytest_cache": true,
}

func forbiddenRootDir(slash string) bool {
	if strings.Contains(slash, "/") {
		return false
	}
	return forbiddenRootDirNames[strings.ToLower(slash)]
}

// forbiddenFileNames are test-runner scratch files. They are not secrets —
// the phpunit one holds test names and timings — but they are output, not
// source, and they change the artifact hash, so the same sample published
// twice gets two content addresses for no reason. One reached the network
// before this check existed.
var forbiddenFileNames = map[string]bool{
	".phpunit.result.cache": true,
	".rspec_status":         true,
	".byebug_history":       true,
	"npm-debug.log":         true,
	"yarn-error.log":        true,
	"erl_crash.dump":        true,
}

// isEnvFile reports whether base names a dotenv secrets file (.env, .env.local, …).
func isEnvFile(base string) bool {
	lower := strings.ToLower(base)
	return lower == ".env" || strings.HasPrefix(lower, ".env.")
}

// BuildArtifact renders dir as the canonical sample artifact: files sorted
// by slash-path, tar mode 0644, uid/gid 0, mtime Unix epoch 0, USTAR only
// (no PAX or user headers), gzip with zero MTime and no name. The returned
// sampleID is domain.SHA256Hex over the tar.gz bytes, so identical trees
// always produce identical IDs regardless of file timestamps.
func BuildArtifact(dir string) (tgz []byte, sampleID string, err error) {
	paths, err := collectFiles(dir)
	if err != nil {
		return nil, "", err
	}
	if len(paths) == 0 {
		return nil, "", errors.New("samples: empty sample directory")
	}
	if len(paths) > MaxFiles {
		return nil, "", fmt.Errorf("samples: %d files exceeds the %d-file limit", len(paths), MaxFiles)
	}
	sort.Strings(paths)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf) // Header: zero MTime, empty Name — canonical.
	tw := tar.NewWriter(gz)
	for _, p := range paths {
		content, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if rerr != nil {
			return nil, "", fmt.Errorf("samples: read %s: %w", p, rerr)
		}
		if bytes.IndexByte(content, 0) >= 0 {
			return nil, "", fmt.Errorf("samples: binary file not allowed: %s", p)
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     p,
			Size:     int64(len(content)),
			Mode:     0o644,
			Uid:      0,
			Gid:      0,
			ModTime:  time.Unix(0, 0),
			Format:   tar.FormatUSTAR,
		}
		if werr := tw.WriteHeader(hdr); werr != nil {
			return nil, "", fmt.Errorf("samples: tar %s: %w", p, werr)
		}
		if _, werr := tw.Write(content); werr != nil {
			return nil, "", fmt.Errorf("samples: tar %s: %w", p, werr)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", fmt.Errorf("samples: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, "", fmt.Errorf("samples: close gzip: %w", err)
	}
	if buf.Len() > MaxCompressedBytes {
		return nil, "", fmt.Errorf("samples: artifact is %d bytes, limit is %d", buf.Len(), MaxCompressedBytes)
	}
	tgz = buf.Bytes()
	return tgz, domain.SHA256Hex(tgz), nil
}

// collectFiles walks dir enforcing the pre-build safety rules and returns
// the sorted-later slash-relative paths of every regular file.
func collectFiles(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if strings.HasPrefix(slash, "..") || path.IsAbs(slash) {
			return fmt.Errorf("samples: path escapes sample dir: %s", slash)
		}
		base := path.Base(slash)
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("samples: symlink not allowed: %s", slash)
		}
		if d.IsDir() {
			if forbiddenDir(base) || forbiddenRootDir(slash) {
				return fmt.Errorf("samples: forbidden entry: %s/", slash)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("samples: non-regular file not allowed: %s", slash)
		}
		if isEnvFile(base) || forbiddenFileNames[strings.ToLower(base)] {
			return fmt.Errorf("samples: forbidden entry: %s", slash)
		}
		paths = append(paths, slash)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// Unpack extracts a sample artifact into destDir with the same safety
// posture as BuildArtifact: clean relative paths only, no traversal or
// absolute names, forbidden entries rejected, compressed and decompressed
// size caps, and no write can land outside destDir.
func Unpack(tgz []byte, destDir string) error {
	if len(tgz) > MaxCompressedBytes {
		return fmt.Errorf("samples: artifact is %d bytes, limit is %d", len(tgz), MaxCompressedBytes)
	}
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return fmt.Errorf("samples: unpack: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var files, entries int
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("samples: unpack: %w", err)
		}
		entries++
		if entries > maxUnpackEntries {
			return fmt.Errorf("samples: unpack: more than %d entries", maxUnpackEntries)
		}
		isDir := hdr.Typeflag == tar.TypeDir
		clean, err := safeEntryName(hdr.Name, isDir)
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, filepath.FromSlash(clean))
		if !within(destDir, target) {
			return fmt.Errorf("samples: unpack: entry %q escapes destination", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("samples: unpack: %w", err)
			}
		case tar.TypeReg:
			files++
			if files > MaxFiles {
				return fmt.Errorf("samples: unpack: more than %d files", MaxFiles)
			}
			if hdr.Size < 0 || hdr.Size > maxUnpackedBytes {
				return fmt.Errorf("samples: unpack: entry %s is %d bytes", clean, hdr.Size)
			}
			total += hdr.Size
			if total > maxUnpackedBytes {
				return fmt.Errorf("samples: unpack: exceeds %d-byte unpacked cap", maxUnpackedBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("samples: unpack: %w", err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return fmt.Errorf("samples: unpack: %w", err)
			}
			_, cerr := io.Copy(f, tr)
			if err := f.Close(); cerr == nil {
				cerr = err
			}
			if cerr != nil {
				return fmt.Errorf("samples: unpack %s: %w", clean, cerr)
			}
		default:
			return fmt.Errorf("samples: unpack: unsupported entry type %d for %q", hdr.Typeflag, hdr.Name)
		}
	}
	return nil
}

// safeEntryName validates one tar entry name and returns its cleaned
// slash-relative form.
func safeEntryName(name string, isDir bool) (string, error) {
	if name == "" ||
		strings.HasPrefix(name, "/") ||
		strings.Contains(name, `\`) ||
		strings.Contains(name, ":") ||
		strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("samples: unpack: unsafe entry name %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("samples: unpack: unsafe entry name %q", name)
	}
	segs := strings.Split(clean, "/")
	for i, seg := range segs {
		if seg == ".." {
			return "", fmt.Errorf("samples: unpack: unsafe entry name %q", name)
		}
		if forbiddenDir(seg) && (i < len(segs)-1 || isDir) {
			return "", fmt.Errorf("samples: unpack: forbidden entry %q", name)
		}
	}
	if !isDir && isEnvFile(segs[len(segs)-1]) {
		return "", fmt.Errorf("samples: unpack: forbidden entry %q", name)
	}
	return clean, nil
}

// within reports whether target stays inside root after cleaning.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
