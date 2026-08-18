package launcher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	Schema          = 1
	ProtocolVersion = "v1.0.0"
	maxPayloadBytes = 256 << 20
)

var canonicalVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type Descriptor struct {
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Sequence uint64 `json:"sequence"`
}

type Active struct {
	Schema       int         `json:"schema"`
	Current      Descriptor  `json:"current"`
	Previous     *Descriptor `json:"previous,omitempty"`
	RollbackHold *Descriptor `json:"rollbackHold,omitempty"`
}

func Path(root string) string { return filepath.Join(root, "active.json") }

func PayloadPath(root, version string) (string, error) {
	if !canonicalVersion.MatchString(version) {
		return "", fmt.Errorf("launcher: noncanonical payload version %q", version)
	}
	return filepath.Join(root, "payloads", version, "csx-payload.exe"), nil
}

func Load(root string) (Active, error) {
	a, err := Read(root)
	if err != nil {
		return Active{}, err
	}
	if err := validateCurrent(root, a); err != nil {
		return Active{}, err
	}
	return a, nil
}

// Read parses and structurally validates the pointer without hashing payloads.
// The launcher itself uses Load before execution; stale-process polling uses
// Read so every MCP tool call does not reread the complete executable.
func Read(root string) (Active, error) {
	path := Path(root)
	fi, err := os.Stat(path)
	if err != nil {
		return Active{}, err
	}
	if fi.Size() > 64<<10 {
		return Active{}, errors.New("launcher: active.json exceeds size limit")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Active{}, err
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return Active{}, err
	}
	var a Active
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return Active{}, fmt.Errorf("launcher: parse active.json: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Active{}, errors.New("launcher: active.json has trailing data")
	}
	if a.Schema != Schema {
		return Active{}, fmt.Errorf("launcher: unsupported active schema %d", a.Schema)
	}
	if err := validateDescriptorShape(a.Current); err != nil {
		return Active{}, err
	}
	if a.Previous != nil {
		if err := validateDescriptorShape(*a.Previous); err != nil {
			return Active{}, err
		}
	}
	if a.RollbackHold != nil {
		if err := validateDescriptorShape(*a.RollbackHold); err != nil {
			return Active{}, err
		}
	}
	return a, nil
}

func rejectDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyTok.(string)
				if !ok {
					return errors.New("launcher: invalid JSON object key")
				}
				if seen[key] {
					return fmt.Errorf("launcher: duplicate JSON key %q", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		default:
			return errors.New("launcher: invalid JSON delimiter")
		}
	}
	return walk()
}

func Validate(root string, a Active) error {
	if err := validateCurrent(root, a); err != nil {
		return err
	}
	if a.Previous != nil {
		if err := validateDescriptor(root, *a.Previous); err != nil {
			return fmt.Errorf("launcher: previous payload: %w", err)
		}
	}
	return nil
}

func validateCurrent(root string, a Active) error {
	if a.Schema != Schema {
		return fmt.Errorf("launcher: unsupported active schema %d", a.Schema)
	}
	if err := validateDescriptor(root, a.Current); err != nil {
		return fmt.Errorf("launcher: current payload: %w", err)
	}
	return nil
}

func validateDescriptor(root string, d Descriptor) error {
	if err := validateDescriptorShape(d); err != nil {
		return err
	}
	path, _ := PayloadPath(root, d.Version)
	if err := validateContainedRegular(root, path); err != nil {
		return err
	}
	sum, err := fileHash(path)
	if err != nil {
		return err
	}
	if sum != d.SHA256 {
		return errors.New("payload SHA-256 mismatch")
	}
	return nil
}

func validateDescriptorShape(d Descriptor) error {
	if !canonicalVersion.MatchString(d.Version) || d.Sequence == 0 || len(d.SHA256) != 64 || strings.ToLower(d.SHA256) != d.SHA256 {
		return errors.New("invalid descriptor")
	}
	if _, err := hex.DecodeString(d.SHA256); err != nil {
		return errors.New("invalid SHA-256")
	}
	return nil
}

func validateContainedRegular(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("payload escapes install root")
	}
	for current := pathAbs; ; current = filepath.Dir(current) {
		fi, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 || hasReparsePoint(current) {
			return errors.New("reparse/symlink path refused")
		}
		if strings.EqualFold(filepath.Clean(current), filepath.Clean(rootAbs)) {
			break
		}
		if parent := filepath.Dir(current); parent == current {
			return errors.New("install root was not reached")
		}
	}
	fi, err := os.Lstat(pathAbs)
	if err != nil || !fi.Mode().IsRegular() {
		return errors.New("payload is not a regular file")
	}
	return nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxPayloadBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxPayloadBytes {
		return "", errors.New("payload exceeds size limit")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func Write(root string, a Active) error {
	if err := Validate(root, a); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".active-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := replacePointer(tmp, Path(root)); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func CommitPayload(root, staged string, d Descriptor) (Active, error) {
	old, loadErr := Load(root)
	if loadErr == nil && old.Current.Version == d.Version && old.Current.SHA256 == d.SHA256 {
		if d.Sequence < old.Current.Sequence {
			return Active{}, errors.New("launcher: refused descriptor sequence rollback")
		}
		if d.Sequence == old.Current.Sequence {
			return old, nil
		}
		old.Current.Sequence = d.Sequence
		if err := Write(root, old); err != nil {
			return Active{}, err
		}
		return old, nil
	}
	final, err := PayloadPath(root, d.Version)
	if err != nil {
		return Active{}, err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(final)), 0o700); err != nil {
		return Active{}, err
	}
	if err := os.Mkdir(filepath.Dir(final), 0o700); err != nil && !os.IsExist(err) {
		return Active{}, err
	}
	if _, err := os.Stat(final); err == nil {
		if err := validateDescriptor(root, d); err != nil {
			return Active{}, errors.New("launcher: immutable version path contains different payload")
		}
	} else if !os.IsNotExist(err) {
		return Active{}, err
	} else if err := promotePayload(staged, final); err != nil {
		return Active{}, err
	}
	if err := validateDescriptor(root, d); err != nil {
		return Active{}, err
	}
	next := Active{Schema: Schema, Current: d}
	if loadErr == nil {
		prev := old.Current
		next.Previous = &prev
	}
	if err := Write(root, next); err != nil {
		return Active{}, err
	}
	return next, nil
}

func ImportPrevious(root, source string, d Descriptor) (Active, error) {
	if err := validateDescriptorShape(d); err != nil {
		return Active{}, err
	}
	f, err := os.Open(source)
	if err != nil {
		return Active{}, err
	}
	defer f.Close()
	tmp, err := os.CreateTemp(root, ".legacy-payload-*.exe")
	if err != nil {
		return Active{}, err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		return Active{}, err
	}
	n, err := io.Copy(tmp, io.LimitReader(f, maxPayloadBytes+1))
	if err != nil {
		_ = tmp.Close()
		return Active{}, err
	}
	if n > maxPayloadBytes {
		_ = tmp.Close()
		return Active{}, errors.New("launcher: legacy payload exceeds size limit")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Active{}, err
	}
	if err := tmp.Close(); err != nil {
		return Active{}, err
	}
	final, err := PayloadPath(root, d.Version)
	if err != nil {
		return Active{}, err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(final)), 0o700); err != nil {
		return Active{}, err
	}
	if err := os.Mkdir(filepath.Dir(final), 0o700); err != nil && !os.IsExist(err) {
		return Active{}, err
	}
	if _, err := os.Stat(final); os.IsNotExist(err) {
		if err := promotePayload(tmpName, final); err != nil {
			return Active{}, err
		}
		cleanup = false
	}
	if err := validateDescriptor(root, d); err != nil {
		return Active{}, err
	}
	a, err := Load(root)
	if err != nil {
		return Active{}, err
	}
	if a.Previous == nil {
		a.Previous = &d
		if err := Write(root, a); err != nil {
			return Active{}, err
		}
	}
	return a, nil
}

func Rollback(root string) (Active, error) {
	a, err := Load(root)
	if err != nil {
		return Active{}, err
	}
	if a.Previous == nil {
		return Active{}, errors.New("launcher: no previous payload")
	}
	if err := validateDescriptor(root, *a.Previous); err != nil {
		return Active{}, fmt.Errorf("launcher: previous payload: %w", err)
	}
	if a.Previous.Sequence >= a.Current.Sequence {
		return Active{}, errors.New("launcher: rollback was already applied or previous payload is not older")
	}
	hold := a.Current
	next := Active{Schema: Schema, Current: *a.Previous, Previous: &a.Current, RollbackHold: &hold}
	if err := Write(root, next); err != nil {
		return Active{}, err
	}
	return next, nil
}
