//go:build !windows

package update

import "os"

func replaceDataFile(staged, target string) error { return os.Rename(staged, target) }

func recoverDataFile(_ string, _ any, original error) error { return original }
