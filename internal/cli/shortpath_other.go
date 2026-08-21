//go:build !windows

package cli

import "strings"

// spaceFreePath has nothing to do off Windows: there is no short-name form,
// and a path with a space in it is simply a path Codex cannot be handed.
func spaceFreePath(p string) (string, bool) {
	if strings.Contains(p, " ") {
		return "", false
	}
	return p, true
}
