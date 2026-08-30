package httpapi

import "github.com/r2cuerdame/codesamplex/internal/domain"

// npmPackagePlatform reads the platform out of a native-binary package name.
//
// The rule itself lives in internal/domain now. It had a copy here and the
// completeness census had a different denominator entirely, so the queue
// refused 399 coordinates on every poll while the backlog counted all 399 as
// work waiting to be done.
func npmPackagePlatform(name string) (string, bool) { return domain.NPMPackagePlatform(name) }
