//go:build windows

package update

import (
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

func acquireNamedLock(path string, wait time.Duration) (func(), error) {
	return launcher.AcquireUpdateLock(path, wait)
}
