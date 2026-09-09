package doctor

import (
	"fmt"
	"path/filepath"
	"strings"
)

// IsPathWithin verifies that target resides strictly within parent directory.
func IsPathWithin(parent, target string) bool {
	if parent == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(target))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// ValidateRepairTarget enforces the CSX trust boundary rule:
// Safe repair implementations are bounded strictly to CSX-owned state ($CSX_HOME, install root).
func ValidateRepairTarget(target string, cctx *CheckContext) error {
	cleanTarget := filepath.Clean(target)
	if cctx.Home != "" && IsPathWithin(cctx.Home, cleanTarget) {
		return nil
	}
	if cctx.Root != "" && IsPathWithin(cctx.Root, cleanTarget) {
		return nil
	}
	// Allow agent configs under AgentHome
	if cctx.AgentHome != "" && IsPathWithin(cctx.AgentHome, cleanTarget) {
		return nil
	}
	return fmt.Errorf("repair target %s is outside CSX-owned boundaries (%s, %s)", target, cctx.Home, cctx.Root)
}
