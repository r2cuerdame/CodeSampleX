//go:build windows

package daemon

// sealInheritedDescriptors has nothing to do on Windows: handle inheritance is
// opt-in per handle rather than the default, so a spawned daemon does not
// silently acquire its parent's pipes the way it did on Linux.
func sealInheritedDescriptors() {}
