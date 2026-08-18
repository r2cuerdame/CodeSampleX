//go:build !windows

package launcher

import "os"

func replacePointer(staged, target string) error { return os.Rename(staged, target) }
func promotePayload(staged, target string) error { return os.Rename(staged, target) }
