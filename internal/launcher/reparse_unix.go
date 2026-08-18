//go:build !windows

package launcher

func hasReparsePoint(string) bool { return false }
