//go:build !cgo

package store

// CgoEnabled is the compiler's own answer to "was this built with cgo", taken
// from the build tag rather than from an environment variable that may have
// been set after the fact.
const CgoEnabled = false
