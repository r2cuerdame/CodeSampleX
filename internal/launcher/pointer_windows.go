//go:build windows

package launcher

import (
	"fmt"
	"syscall"
	"unsafe"
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replacePointer(staged, target string) error {
	from, err := syscall.UTF16PtrFromString(staged)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r1, _, callErr := moveFileExW.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 0x1|0x8)
	if r1 == 0 {
		return fmt.Errorf("launcher: durable active pointer replace: %w", callErr)
	}
	return nil
}

func promotePayload(staged, target string) error {
	from, err := syscall.UTF16PtrFromString(staged)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r1, _, callErr := moveFileExW.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 0x8)
	if r1 == 0 {
		return fmt.Errorf("launcher: durable immutable payload promote: %w", callErr)
	}
	return nil
}
