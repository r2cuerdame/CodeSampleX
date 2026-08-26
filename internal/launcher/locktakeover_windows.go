//go:build windows

package launcher

import (
	"errors"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type recoveryLockAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

func recoveryLockHandleIsReparse(h windows.Handle) (bool, error) {
	var attrs recoveryLockAttributeTagInfo
	err := windows.GetFileInformationByHandleEx(
		h,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&attrs)),
		uint32(unsafe.Sizeof(attrs)),
	)
	return attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, err
}

// tryTakeOverRecoveryInstallLock deletes only the exact stale file that was
// inspected. Holding a handle opened without FILE_SHARE_DELETE pins that file
// identity: no contender can replace the pathname between the liveness check
// and FileDispositionInfo. Once the handle closes, the caller retries O_EXCL;
// if somebody else wins, their new live file is inspected independently and
// is never deleted based on the stale predecessor's contents.
func tryTakeOverRecoveryInstallLock(path, _ string) (unlock func(), acquired, retry bool, err error) {
	// Do not take a DELETE-capable identity pin for an ordinary live owner: on
	// Windows that can repeatedly collide with the owner's bounded Remove on
	// release. This first check is only a hint. A stale result is revalidated
	// below from the pinned handle before any deletion is requested.
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, false, err
	}
	preHandle, openErr := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if openErr != nil {
		if errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) || errors.Is(openErr, windows.ERROR_PATH_NOT_FOUND) {
			return nil, false, true, nil
		}
		return nil, false, false, nil
	}
	pre := os.NewFile(uintptr(preHandle), path)
	if pre == nil {
		_ = windows.CloseHandle(preHandle)
		return nil, false, false, errors.New("launcher: wrap install update lock probe handle")
	}
	preReparse, preAttrErr := recoveryLockHandleIsReparse(preHandle)
	preRaw, preReadErr := io.ReadAll(io.LimitReader(pre, 4<<10))
	preInfo, preStatErr := pre.Stat()
	_ = pre.Close()
	if preAttrErr != nil || preReparse || preReadErr != nil || preStatErr != nil || !recoveryInstallLockRecordIsStale(preRaw, preInfo.ModTime()) {
		return nil, false, false, nil
	}

	h, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, false, true, nil
		}
		// A live owner or scanner may hold a handle that does not share delete.
		// That is not proof of staleness, so fail conservatively and wait.
		return nil, false, false, nil
	}
	f := os.NewFile(uintptr(h), path)
	if f == nil {
		_ = windows.CloseHandle(h)
		return nil, false, false, errors.New("launcher: wrap install update lock handle")
	}
	reparse, attrErr := recoveryLockHandleIsReparse(h)
	if attrErr != nil || reparse {
		_ = f.Close()
		return nil, false, false, nil
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, 4<<10))
	fi, statErr := f.Stat()
	if readErr != nil || statErr != nil || !recoveryInstallLockRecordIsStale(raw, fi.ModTime()) {
		_ = f.Close()
		return nil, false, false, nil
	}

	recoveryLockBeforeDisposition()
	deleteFile := byte(1)
	if err := windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &deleteFile, 1); err != nil {
		_ = f.Close()
		return nil, false, false, nil
	}
	// FileDispositionInfo marks this opened file, not whatever may later occupy
	// the same pathname. Close completes the deletion and releases the identity
	// pin; a fresh O_EXCL then decides the next owner.
	_ = f.Close()
	return nil, false, true, nil
}
