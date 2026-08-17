//go:build genericprobe

package generic

import "golang.org/x/sync/syncmap"

// syncmap.Map is a type alias for sync.Map, not a parameterized map type.
var _ syncmap.Map[string, int]
