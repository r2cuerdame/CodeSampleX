package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// The cache is short lived because edits to source do not change a lockfile.
// Its identity includes the project, resolved dependencies and ignore rules.
func symbolCachePath(dir string, pkgs []scanner.ResolvedPackage) string {
	root, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	h := sha256.New()
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	h.Write([]byte("node-symbols-v3\x00" + abs + "\x00"))
	lockFound := false
	for _, name := range []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock"} {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			h.Write([]byte(name))
			h.Write(data)
			lockFound = true
			break
		}
	}
	if !lockFound {
		return ""
	}
	if data, err := os.ReadFile(filepath.Join(dir, ".gitignore")); err == nil {
		h.Write(data)
	}
	for _, p := range pkgs {
		h.Write([]byte(p.PURL.String() + "\x00"))
	}
	return filepath.Join(root, "csx", "symbols", hex.EncodeToString(h.Sum(nil))+".json")
}

func readSymbolCache(path string) ([]scanner.SymbolUsage, bool) {
	if path == "" {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > 30*time.Second {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var uses []scanner.SymbolUsage
	if json.Unmarshal(data, &uses) != nil {
		return nil, false
	}
	return uses, true
}

func writeSymbolCache(path string, uses []scanner.SymbolUsage) {
	if path == "" {
		return
	}
	data, err := json.Marshal(uses)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "symbols-*.tmp")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err = tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}
